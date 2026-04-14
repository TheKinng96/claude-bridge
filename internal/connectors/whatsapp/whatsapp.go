package whatsapp

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "claude-bridge/internal/store" // registers "sqlite-fk" driver
	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"claude-bridge/internal/store"
)

// Message represents a WhatsApp message (sent or received).
type Message struct {
	ID        string    `json:"id"`
	ChatJID   string    `json:"chat_jid"`
	SenderJID string    `json:"sender_jid"`
	PushName  string    `json:"push_name"`
	Body      string    `json:"body"`
	FromMe    bool      `json:"from_me"`
	Timestamp time.Time `json:"timestamp"`
	AccountJID string   `json:"account_jid"` // which connected account received this
}

// Contact represents a WhatsApp contact.
type Contact struct {
	JID        string    `json:"jid"`
	PushName   string    `json:"push_name"`
	ProfilePic string    `json:"profile_pic,omitempty"`
	LastSeen   time.Time `json:"last_seen"`
}

// AccountClient is a single WhatsApp account connection.
type AccountClient struct {
	mu         sync.RWMutex
	client     *whatsmeow.Client
	connected  bool
	jid        string
	phone      string
	pushName   string
	profilePic string

	messages []Message
	contacts map[string]*Contact
}

// Manager manages multiple WhatsApp account connections.
type Manager struct {
	mu        sync.RWMutex
	clients   map[string]*AccountClient // keyed by JID string
	container *sqlstore.Container
	appStore  *store.Store
	dataDir   string

	// QR flow state (only one QR flow at a time).
	qrMu     sync.Mutex
	qrCode   string
	qrError  string
	qrActive bool
}

// NewManager creates a new WhatsApp multi-account manager.
func NewManager(dataDir string, appStore *store.Store) *Manager {
	return &Manager{
		clients:  make(map[string]*AccountClient),
		appStore: appStore,
		dataDir:  dataDir,
	}
}

// initContainer ensures the whatsmeow SQLite container is ready.
func (m *Manager) initContainer() error {
	if m.container != nil {
		return nil
	}

	if err := os.MkdirAll(m.dataDir, 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(m.dataDir, "whatsapp.db")
	dbURI := fmt.Sprintf("file:%s", dbPath)
	logger := waLog.Stdout("whatsmeow", "INFO", true)

	container, err := sqlstore.New(context.Background(), "sqlite3", dbURI, logger)
	if err != nil {
		return fmt.Errorf("init sqlstore: %w", err)
	}
	m.container = container
	log.Printf("[whatsapp] Session store: %s", dbPath)
	return nil
}

// Boot reconnects all accounts that were previously connected. Call on startup.
func (m *Manager) Boot() error {
	if err := m.initContainer(); err != nil {
		return err
	}

	accounts, err := m.appStore.GetAccounts(context.Background(), "whatsapp")
	if err != nil {
		return fmt.Errorf("get accounts: %w", err)
	}

	for _, acct := range accounts {
		log.Printf("[whatsapp] Found saved account: %s (%s)", acct.PushName, acct.PhoneNumber)
		// Try to reconnect accounts that have a stored session.
		go func(jid string) {
			if err := m.Reconnect(jid); err != nil {
				log.Printf("[whatsapp] Auto-reconnect %s failed: %v", jid, err)
				m.appStore.SetStatus(context.Background(), jid, "disconnected")
			}
		}(acct.JID)
	}

	return nil
}

// Reconnect connects an existing account using its stored session (no QR needed).
func (m *Manager) Reconnect(jid string) error {
	if err := m.initContainer(); err != nil {
		return err
	}

	// Check if already connected.
	m.mu.RLock()
	if ac, ok := m.clients[jid]; ok {
		ac.mu.RLock()
		connected := ac.connected
		ac.mu.RUnlock()
		if connected {
			m.mu.RUnlock()
			return nil // already connected
		}
	}
	m.mu.RUnlock()

	// Find the device in the whatsmeow store.
	devices, err := m.container.GetAllDevices(context.Background())
	if err != nil {
		return fmt.Errorf("get devices: %w", err)
	}

	var deviceStore *waStore.Device
	for _, d := range devices {
		if d.ID != nil && d.ID.String() == jid {
			deviceStore = d
			break
		}
		// Also match by user part (phone number in JID).
		if d.ID != nil {
			fullJID := types.NewJID(d.ID.User, types.DefaultUserServer).String()
			if fullJID == jid {
				deviceStore = d
				break
			}
		}
	}

	if deviceStore == nil {
		return fmt.Errorf("no stored session for %s — need QR scan", jid)
	}

	logger := waLog.Stdout("whatsmeow", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, logger)

	ac := &AccountClient{
		jid:      jid,
		contacts: make(map[string]*Contact),
	}

	client.AddEventHandler(func(evt interface{}) {
		m.handleEvent(ac, client, evt)
	})

	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	ac.mu.Lock()
	ac.client = client
	ac.mu.Unlock()

	m.mu.Lock()
	m.clients[jid] = ac
	m.mu.Unlock()

	return nil
}

// StartQR initiates a new QR code login flow for adding a new account.
func (m *Manager) StartQR() error {
	m.qrMu.Lock()
	if m.qrActive {
		m.qrMu.Unlock()
		return fmt.Errorf("QR flow already active")
	}
	m.qrActive = true
	m.qrCode = ""
	m.qrError = ""
	m.qrMu.Unlock()

	if err := m.initContainer(); err != nil {
		m.clearQR()
		return err
	}

	deviceStore := m.container.NewDevice()
	logger := waLog.Stdout("whatsmeow", "INFO", true)
	client := whatsmeow.NewClient(deviceStore, logger)

	qrChan, err := client.GetQRChannel(context.Background())
	if err != nil {
		m.clearQR()
		return fmt.Errorf("get QR channel: %w", err)
	}

	if err := client.Connect(); err != nil {
		m.clearQR()
		return fmt.Errorf("connect for QR: %w", err)
	}

	log.Println("[whatsapp] QR flow started — waiting for scan")

	go m.processQRFlow(client, qrChan)

	return nil
}

func (m *Manager) processQRFlow(client *whatsmeow.Client, ch <-chan whatsmeow.QRChannelItem) {
	defer m.clearQR()

	for evt := range ch {
		log.Printf("[whatsapp] QR event: %s", evt.Event)

		switch evt.Event {
		case "code":
			png, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
			if err != nil {
				log.Printf("[whatsapp] QR encode error: %v", err)
				continue
			}
			dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
			m.qrMu.Lock()
			m.qrCode = dataURI
			m.qrError = ""
			m.qrMu.Unlock()
			log.Printf("[whatsapp] QR code ready")

		case "login", "success":
			log.Println("[whatsapp] QR scanned — login successful!")
			m.qrMu.Lock()
			m.qrCode = ""
			m.qrMu.Unlock()

			// Register this new account.
			if client.Store.ID != nil {
				jid := types.NewJID(client.Store.ID.User, types.DefaultUserServer).String()
				ac := &AccountClient{
					jid:      jid,
					phone:    client.Store.ID.User,
					pushName: client.Store.PushName,
					contacts: make(map[string]*Contact),
				}

				client.AddEventHandler(func(evt interface{}) {
					m.handleEvent(ac, client, evt)
				})

				ac.mu.Lock()
				ac.client = client
				ac.connected = true
				ac.mu.Unlock()

				m.mu.Lock()
				m.clients[jid] = ac
				m.mu.Unlock()

				// Persist to app store.
				m.appStore.UpsertAccount(context.Background(), &store.Account{
					Channel:     "whatsapp",
					JID:         jid,
					PhoneNumber: client.Store.ID.User,
					PushName:    client.Store.PushName,
					Status:      "connected",
					LastSeenAt:  time.Now(),
				})

				log.Printf("[whatsapp] New account added: %s (%s)", client.Store.PushName, client.Store.ID.User)

				// Fetch profile pic in background.
				go m.fetchProfilePic(ac, client)
			}
			return

		case "timeout":
			m.qrMu.Lock()
			m.qrCode = ""
			m.qrError = "QR code timed out — try again"
			m.qrMu.Unlock()
			return

		case "error", "err-client-outdated":
			m.qrMu.Lock()
			m.qrCode = ""
			m.qrError = fmt.Sprintf("QR error: %s", evt.Event)
			m.qrMu.Unlock()
			return
		}
	}
}

func (m *Manager) clearQR() {
	m.qrMu.Lock()
	m.qrActive = false
	m.qrCode = ""
	m.qrMu.Unlock()
}

// handleEvent processes whatsmeow events for a specific account.
func (m *Manager) handleEvent(ac *AccountClient, client *whatsmeow.Client, evt interface{}) {
	switch v := evt.(type) {
	case *events.Connected:
		log.Printf("[whatsapp] %s: Connected", ac.jid)
		ac.mu.Lock()
		ac.connected = true
		if client.Store.ID != nil {
			ac.phone = client.Store.ID.User
			ac.pushName = client.Store.PushName
		}
		ac.mu.Unlock()

		m.appStore.SetStatus(context.Background(), ac.jid, "connected")
		go m.fetchProfilePic(ac, client)

	case *events.Disconnected:
		log.Printf("[whatsapp] %s: Disconnected", ac.jid)
		ac.mu.Lock()
		ac.connected = false
		ac.mu.Unlock()
		m.appStore.SetStatus(context.Background(), ac.jid, "disconnected")

	case *events.LoggedOut:
		log.Printf("[whatsapp] %s: LoggedOut", ac.jid)
		ac.mu.Lock()
		ac.connected = false
		ac.mu.Unlock()
		m.appStore.SetStatus(context.Background(), ac.jid, "disconnected")

	case *events.Message:
		m.handleMessage(ac, v)
	}
}

func (m *Manager) fetchProfilePic(ac *AccountClient, client *whatsmeow.Client) {
	if client.Store.ID == nil {
		return
	}
	ownJID := types.NewJID(client.Store.ID.User, types.DefaultUserServer)
	pic, err := client.GetProfilePictureInfo(context.Background(), ownJID, &whatsmeow.GetProfilePictureParams{})
	if err != nil {
		log.Printf("[whatsapp] %s: Could not fetch profile pic: %v", ac.jid, err)
		return
	}
	if pic != nil {
		ac.mu.Lock()
		ac.profilePic = pic.URL
		ac.mu.Unlock()
		m.appStore.SetProfilePic(context.Background(), ac.jid, pic.URL)
		log.Printf("[whatsapp] %s: Profile pic fetched", ac.jid)
	}
}

func (m *Manager) handleMessage(ac *AccountClient, evt *events.Message) {
	body := ""
	if evt.Message.GetConversation() != "" {
		body = evt.Message.GetConversation()
	} else if evt.Message.GetExtendedTextMessage() != nil {
		body = evt.Message.GetExtendedTextMessage().GetText()
	}

	msg := Message{
		ID:         evt.Info.ID,
		ChatJID:    evt.Info.Chat.String(),
		SenderJID:  evt.Info.Sender.String(),
		PushName:   evt.Info.PushName,
		Body:       body,
		FromMe:     evt.Info.IsFromMe,
		Timestamp:  evt.Info.Timestamp,
		AccountJID: ac.jid,
	}

	if body != "" {
		log.Printf("[whatsapp] %s msg from %s: %s", ac.jid, msg.SenderJID, truncate(body, 50))
	}

	ac.mu.Lock()
	ac.messages = append(ac.messages, msg)
	if len(ac.messages) > 1000 {
		ac.messages = ac.messages[len(ac.messages)-1000:]
	}

	senderKey := evt.Info.Sender.String()
	if !evt.Info.IsFromMe && senderKey != "" {
		if ac.contacts == nil {
			ac.contacts = make(map[string]*Contact)
		}
		ac.contacts[senderKey] = &Contact{
			JID:      senderKey,
			PushName: evt.Info.PushName,
			LastSeen: evt.Info.Timestamp,
		}
	}
	ac.mu.Unlock()
}

// --- Public API ---

// QRCode returns the current QR data URI, or "".
func (m *Manager) QRCode() string {
	m.qrMu.Lock()
	defer m.qrMu.Unlock()
	return m.qrCode
}

// QRError returns the last QR flow error, or "".
func (m *Manager) QRError() string {
	m.qrMu.Lock()
	defer m.qrMu.Unlock()
	return m.qrError
}

// QRActive returns whether a QR flow is in progress.
func (m *Manager) QRActive() bool {
	m.qrMu.Lock()
	defer m.qrMu.Unlock()
	return m.qrActive
}

// GetAccounts returns all known WhatsApp accounts with live status.
func (m *Manager) GetAccounts() []store.Account {
	accounts, err := m.appStore.GetAccounts(context.Background(), "whatsapp")
	if err != nil {
		log.Printf("[whatsapp] Error getting accounts: %v", err)
		return nil
	}

	// Overlay live connection status.
	// If a client exists in memory, use its live status.
	// If not in memory, mark as disconnected (DB may have stale "connected" status).
	m.mu.RLock()
	for i := range accounts {
		if ac, ok := m.clients[accounts[i].JID]; ok {
			ac.mu.RLock()
			if ac.connected {
				accounts[i].Status = "connected"
			} else {
				accounts[i].Status = "disconnected"
			}
			if ac.profilePic != "" {
				accounts[i].ProfilePic = ac.profilePic
			}
			if ac.pushName != "" {
				accounts[i].PushName = ac.pushName
			}
			ac.mu.RUnlock()
		} else {
			// No live client — account exists in DB but not running
			accounts[i].Status = "disconnected"
		}
	}
	m.mu.RUnlock()

	return accounts
}

// Disconnect disconnects a specific account.
func (m *Manager) Disconnect(jid string) {
	m.mu.RLock()
	ac, ok := m.clients[jid]
	m.mu.RUnlock()

	if ok {
		ac.mu.Lock()
		if ac.client != nil {
			ac.client.Disconnect()
		}
		ac.connected = false
		ac.mu.Unlock()
	}

	m.appStore.SetStatus(context.Background(), jid, "disconnected")
	log.Printf("[whatsapp] %s: Disconnected by user", jid)
}

// RemoveAccount disconnects and removes an account entirely.
func (m *Manager) RemoveAccount(jid string) {
	m.Disconnect(jid)

	m.mu.Lock()
	if ac, ok := m.clients[jid]; ok {
		ac.mu.Lock()
		if ac.client != nil {
			ac.client.Logout(context.Background())
		}
		ac.mu.Unlock()
		delete(m.clients, jid)
	}
	m.mu.Unlock()

	m.appStore.DeleteAccount(context.Background(), jid)
	log.Printf("[whatsapp] %s: Account removed", jid)
}

// DisconnectAll disconnects all accounts. Call on shutdown.
func (m *Manager) DisconnectAll() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ac := range m.clients {
		ac.mu.Lock()
		if ac.client != nil {
			ac.client.Disconnect()
		}
		ac.connected = false
		ac.mu.Unlock()
	}
}

// SendMessage sends a message from the first connected account (or a specific one).
func (m *Manager) SendMessage(phone string, text string, fromJID string) error {
	ac, client := m.getConnectedClient(fromJID)
	if ac == nil {
		return fmt.Errorf("no connected WhatsApp account")
	}

	targetJID, err := types.ParseJID(phone + "@s.whatsapp.net")
	if err != nil {
		return fmt.Errorf("invalid phone %q: %w", phone, err)
	}

	msg := &waE2E.Message{
		Conversation: proto.String(text),
	}

	resp, err := client.SendMessage(context.Background(), targetJID, msg)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}

	log.Printf("[whatsapp] %s: Sent to %s (ID: %s)", ac.jid, phone, resp.ID)

	outMsg := Message{
		ID:         resp.ID,
		ChatJID:    targetJID.String(),
		SenderJID:  client.Store.ID.String(),
		Body:       text,
		FromMe:     true,
		Timestamp:  resp.Timestamp,
		AccountJID: ac.jid,
	}

	ac.mu.Lock()
	ac.messages = append(ac.messages, outMsg)
	ac.mu.Unlock()

	return nil
}

// GetMessages returns messages across all accounts, optionally filtered.
func (m *Manager) GetMessages(chatJID string, limit int) []Message {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var all []Message
	for _, ac := range m.clients {
		ac.mu.RLock()
		for _, msg := range ac.messages {
			if chatJID == "" || msg.ChatJID == chatJID {
				all = append(all, msg)
			}
		}
		ac.mu.RUnlock()
	}

	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

// GetContacts returns contacts from all accounts, merged with whatsmeow store.
func (m *Manager) GetContacts() []Contact {
	m.mu.RLock()
	defer m.mu.RUnlock()

	merged := make(map[string]*Contact)

	for _, ac := range m.clients {
		// Pull from whatsmeow's contact store.
		ac.mu.RLock()
		client := ac.client
		ac.mu.RUnlock()

		if client != nil && client.Store.Contacts != nil {
			allContacts, err := client.Store.Contacts.GetAllContacts(context.Background())
			if err == nil {
				for jid, info := range allContacts {
					name := info.PushName
					if name == "" {
						name = info.BusinessName
					}
					if name == "" {
						name = info.FullName
					}
					merged[jid.String()] = &Contact{
						JID:      jid.String(),
						PushName: name,
					}
				}
			}
		}

		// Overlay live contacts.
		ac.mu.RLock()
		for k, ct := range ac.contacts {
			if existing, ok := merged[k]; ok {
				if ct.PushName != "" {
					existing.PushName = ct.PushName
				}
				existing.LastSeen = ct.LastSeen
				existing.ProfilePic = ct.ProfilePic
			} else {
				merged[k] = ct
			}
		}
		ac.mu.RUnlock()
	}

	out := make([]Contact, 0, len(merged))
	for _, ct := range merged {
		out = append(out, *ct)
	}
	return out
}

// ConnectedCount returns the number of currently connected accounts.
func (m *Manager) ConnectedCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, ac := range m.clients {
		ac.mu.RLock()
		if ac.connected {
			n++
		}
		ac.mu.RUnlock()
	}
	return n
}

// MessageCount returns total messages across all accounts.
func (m *Manager) MessageCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, ac := range m.clients {
		ac.mu.RLock()
		n += len(ac.messages)
		ac.mu.RUnlock()
	}
	return n
}

// ContactCount returns total unique contacts across all accounts.
func (m *Manager) ContactCount() int {
	return len(m.GetContacts())
}

func (m *Manager) getConnectedClient(preferJID string) (*AccountClient, *whatsmeow.Client) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// If a specific JID is requested, use it.
	if preferJID != "" {
		if ac, ok := m.clients[preferJID]; ok {
			ac.mu.RLock()
			client := ac.client
			connected := ac.connected
			ac.mu.RUnlock()
			if connected && client != nil {
				return ac, client
			}
		}
	}

	// Otherwise, use first connected account.
	for _, ac := range m.clients {
		ac.mu.RLock()
		client := ac.client
		connected := ac.connected
		ac.mu.RUnlock()
		if connected && client != nil {
			return ac, client
		}
	}
	return nil, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
