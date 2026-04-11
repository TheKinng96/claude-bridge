package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"crm-agent/internal/connectors/facebook"
	"crm-agent/internal/connectors/whatsapp"
	"crm-agent/internal/mcp"
)

// Server is the HTTP server that serves the UI and API.
type Server struct {
	wa          *whatsapp.Manager
	fb          *facebook.Connector
	port        int
	listener    net.Listener
	tlsListener net.Listener
	mu          sync.Mutex
}

// New creates a new server. Pass the connectors so the API can interact with them.
func New(wa *whatsapp.Manager, fb *facebook.Connector, port int) *Server {
	return &Server{wa: wa, fb: fb, port: port}
}

// buildMux creates the HTTP route mux. Shared between HTTP and HTTPS servers.
func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()

	// Shared static assets (theme CSS/JS)
	mux.HandleFunc("/static/theme.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		fmt.Fprint(w, sharedCSS)
	})
	mux.HandleFunc("/static/theme.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		fmt.Fprint(w, sharedJS)
	})

	// Pages
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/setup/whatsapp", s.handleWhatsApp)
	mux.HandleFunc("/setup/whatsapp-business", s.handleWhatsAppBusiness)
	mux.HandleFunc("/setup/facebook", s.handleFacebook)

	// API — general status
	mux.HandleFunc("/api/status", s.handleAPIStatus)

	// API — WhatsApp multi-account endpoints
	mux.HandleFunc("/api/whatsapp/accounts", s.handleWAAccounts)
	mux.HandleFunc("/api/whatsapp/accounts/qr", s.handleWAStartQR)
	mux.HandleFunc("/api/whatsapp/accounts/reconnect", s.handleWAReconnect)
	mux.HandleFunc("/api/whatsapp/accounts/disconnect", s.handleWADisconnectAccount)
	mux.HandleFunc("/api/whatsapp/accounts/remove", s.handleWARemoveAccount)
	mux.HandleFunc("/api/whatsapp/qr", s.handleWAQR)

	// API — Claude Desktop auto-install
	mux.HandleFunc("/api/claude/status", s.handleClaudeInstallCheck)
	mux.HandleFunc("/api/claude/install", s.handleClaudeInstall)
	mux.HandleFunc("/api/claude/uninstall", s.handleClaudeUninstall)

	// API — Facebook
	mux.HandleFunc("/api/facebook/connect", s.handleFBConnect)
	mux.HandleFunc("/api/facebook/disconnect", s.handleFBDisconnect)

	// MCP-related API endpoints (used by the MCP server to proxy into the running app)
	mux.HandleFunc("/api/whatsapp/send", s.handleWASend)
	mux.HandleFunc("/api/whatsapp/messages", s.handleWAMessages)
	mux.HandleFunc("/api/whatsapp/contacts", s.handleWAContacts)

	// MCP SSE endpoints — available for remote MCP clients
	mcpHandler := mcp.NewSSEHandler(fmt.Sprintf("http://127.0.0.1:%d", s.port))
	mux.HandleFunc("/mcp/sse", mcpHandler.HandleSSE)
	mux.HandleFunc("/mcp/message", mcpHandler.HandleMessage)

	return mux
}

// Start begins listening on HTTP. It returns immediately; the server runs in the background.
func (s *Server) Start() error {
	mux := s.buildMux()

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	go func() {
		log.Printf("CRM Agent HTTP  server running at http://%s", addr)
		if err := http.Serve(ln, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Addr returns the listen address.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Stop shuts down both HTTP and HTTPS servers.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		s.listener.Close()
	}
	if s.tlsListener != nil {
		s.tlsListener.Close()
	}
}

// --- Page handlers ---

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

func (s *Server) handleWhatsApp(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, whatsappHTML)
}

func (s *Server) handleWhatsAppBusiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, whatsappBusinessHTML)
}

func (s *Server) handleFacebook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, facebookHTML)
}

// --- API handlers ---

type statusResponse struct {
	WhatsApp     waStatusResp `json:"whatsapp"`
	Facebook     fbStatus     `json:"facebook"`
	MessageCount int          `json:"message_count"`
	ContactCount int          `json:"contact_count"`
}

type waStatusResp struct {
	Connected      bool   `json:"connected"`
	ConnectedCount int    `json:"connected_count"`
	TotalAccounts  int    `json:"total_accounts"`
	QRActive       bool   `json:"qr_active"`
	QRCode         string `json:"qr_code,omitempty"`
	QRError        string `json:"qr_error,omitempty"`
}

type fbStatus struct {
	Connected bool   `json:"connected"`
	PageName  string `json:"page_name,omitempty"`
}

func (s *Server) handleAPIStatus(w http.ResponseWriter, r *http.Request) {
	accounts := s.wa.GetAccounts()
	connectedCount := s.wa.ConnectedCount()

	resp := statusResponse{
		WhatsApp: waStatusResp{
			Connected:      connectedCount > 0,
			ConnectedCount: connectedCount,
			TotalAccounts:  len(accounts),
			QRActive:       s.wa.QRActive(),
			QRCode:         s.wa.QRCode(),
			QRError:        s.wa.QRError(),
		},
		Facebook: fbStatus{
			Connected: s.fb.IsConnected(),
			PageName:  s.fb.PageName(),
		},
		MessageCount: s.wa.MessageCount(),
		ContactCount: s.wa.ContactCount(),
	}
	writeJSON(w, resp)
}

// handleWAAccounts returns the list of all WhatsApp accounts.
func (s *Server) handleWAAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleWARemoveAccount(w, r)
		return
	}
	accounts := s.wa.GetAccounts()
	writeJSON(w, map[string]interface{}{
		"accounts": accounts,
		"qr_active": s.wa.QRActive(),
		"qr_code":   s.wa.QRCode(),
		"qr_error":  s.wa.QRError(),
	})
}

// handleWAStartQR starts a new QR flow for adding a new account.
func (s *Server) handleWAStartQR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := s.wa.StartQR(); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleWAReconnect reconnects an existing account by JID.
func (s *Server) handleWAReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		var body struct {
			JID string `json:"jid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		jid = body.JID
	}
	if jid == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "jid required"})
		return
	}
	if err := s.wa.Reconnect(jid); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleWADisconnectAccount disconnects a specific account.
func (s *Server) handleWADisconnectAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		var body struct {
			JID string `json:"jid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		jid = body.JID
	}
	if jid == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "jid required"})
		return
	}
	s.wa.Disconnect(jid)
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleWARemoveAccount removes an account entirely.
func (s *Server) handleWARemoveAccount(w http.ResponseWriter, r *http.Request) {
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		var body struct {
			JID string `json:"jid"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		jid = body.JID
	}
	if jid == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "jid required"})
		return
	}
	s.wa.RemoveAccount(jid)
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleWAQR(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"qr_code":   s.wa.QRCode(),
		"qr_active": s.wa.QRActive(),
		"qr_error":  s.wa.QRError(),
	})
}

func (s *Server) handleFBConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PageID string `json:"page_id"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if err := s.fb.Connect(body.PageID, body.Token); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleFBDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.fb.Disconnect()
	writeJSON(w, map[string]interface{}{"ok": true})
}

// --- MCP proxy API endpoints ---

func (s *Server) handleWASend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Phone   string `json:"phone"`
		Message string `json:"message"`
		FromJID string `json:"from_jid,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Phone == "" || body.Message == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "phone and message required"})
		return
	}
	if err := s.wa.SendMessage(body.Phone, body.Message, body.FromJID); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleWAMessages(w http.ResponseWriter, r *http.Request) {
	chatJID := r.URL.Query().Get("chat")
	limit := 50
	msgs := s.wa.GetMessages(chatJID, limit)
	writeJSON(w, map[string]interface{}{"messages": msgs})
}

func (s *Server) handleWAContacts(w http.ResponseWriter, r *http.Request) {
	contacts := s.wa.GetContacts()

	// Support ?limit=N and ?q=search for the MCP tool.
	q := strings.ToLower(r.URL.Query().Get("q"))
	if q != "" {
		var filtered []whatsapp.Contact
		for _, c := range contacts {
			if strings.Contains(strings.ToLower(c.PushName), q) || strings.Contains(c.JID, q) {
				filtered = append(filtered, c)
			}
		}
		contacts = filtered
	}

	limitStr := r.URL.Query().Get("limit")
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n < len(contacts) {
			contacts = contacts[:n]
		}
	}

	writeJSON(w, map[string]interface{}{
		"contacts": contacts,
		"total":    len(contacts),
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
