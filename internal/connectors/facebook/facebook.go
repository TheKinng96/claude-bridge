// Package facebook provides a browser-based Facebook connector.
// It automates login, posting to Pages, and reading/sending Messenger messages
// using go-rod browser automation via the shared browser engine.
package facebook

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/browser"
	"claude-bridge/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

const (
	platformName    = "facebook"
	loginURL        = "https://www.facebook.com/login"
	messengerURL    = "https://www.messenger.com"
	facebookBaseURL = "https://www.facebook.com"
	loginTimeout    = 30 * time.Second
	actionTimeout   = 20 * time.Second
	healthTimeout   = 15 * time.Second
)

// PageInfo holds information about a connected Facebook Page.
type PageInfo struct {
	PageID   string `json:"page_id"`
	PageName string `json:"page_name"`
	PageURL  string `json:"page_url"`
}

// Connector manages Facebook: browser automation for posting, Graph API for Messenger.
type Connector struct {
	mu      sync.RWMutex
	engine  *browser.Engine
	store   *store.Store
	logger  *log.Logger

	loggedIn bool
	userName string
	pageInfo *PageInfo

	// Messenger API (Graph API based)
	Messenger *MessengerAPI
}

// New creates a new Facebook connector.
func New(engine *browser.Engine, appStore *store.Store) *Connector {
	return &Connector{
		engine:    engine,
		store:     appStore,
		logger:    log.Default(),
		Messenger: NewMessengerAPI(appStore),
	}
}

// Boot loads saved OAuth config and starts polling if connected.
func (c *Connector) Boot(ctx context.Context) {
	if err := c.Messenger.LoadOAuthConfig(ctx); err != nil {
		c.logger.Printf("[facebook] Warning: failed to load OAuth config: %v", err)
	}
	if c.Messenger.IsConnected() {
		pt := c.Messenger.GetPageInfo()
		c.logger.Printf("[facebook] Messenger connected to page: %s", pt.PageName)
		c.Messenger.StartPolling(ctx)
	}
}

// IsConnected returns whether Facebook is logged in.
func (c *Connector) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loggedIn
}

// UserName returns the logged-in user's name.
func (c *Connector) UserName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userName
}

// PageName returns the connected page name if any.
func (c *Connector) PageName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pageInfo != nil {
		return c.pageInfo.PageName
	}
	return ""
}

// GetPageInfo returns the connected page info.
func (c *Connector) GetPageInfo() *PageInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pageInfo
}

// SaveCredentials stores Facebook login credentials.
func (c *Connector) SaveCredentials(ctx context.Context, email, password string, extra map[string]string) error {
	extraJSON, _ := json.Marshal(extra)
	return c.store.SaveCredential(ctx, &store.Credential{
		Platform: platformName,
		Email:    email,
		Password: password,
		Extra:    string(extraJSON),
	})
}

// GetCredentials returns saved credentials.
func (c *Connector) GetCredentials(ctx context.Context) (*store.Credential, error) {
	return c.store.GetCredential(ctx, platformName)
}

// Login performs browser-based login to Facebook.
func (c *Connector) Login(ctx context.Context) error {
	cred, err := c.store.GetCredential(ctx, platformName)
	if err != nil {
		return fmt.Errorf("get credentials: %w", err)
	}
	if cred == nil || cred.Email == "" || cred.Password == "" {
		return fmt.Errorf("no Facebook credentials saved — add them in the dashboard first")
	}

	if err := c.engine.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	c.logger.Printf("[facebook] Logging in as %s...", cred.Email)

	if err := c.engine.NavigateAndWait(page, loginURL, loginTimeout); err != nil {
		return fmt.Errorf("navigate login: %w", err)
	}

	browser.HumanDelay()

	// Accept cookie banner if present
	c.dismissCookieBanner(page)

	// Fill email
	emailEl, err := page.Timeout(actionTimeout).Element("#email")
	if err != nil {
		return fmt.Errorf("find email field: %w", err)
	}
	if err := browser.TypeLikeHuman(emailEl, cred.Email); err != nil {
		return fmt.Errorf("type email: %w", err)
	}

	browser.ShortDelay()

	// Fill password
	passEl, err := page.Timeout(actionTimeout).Element("#pass")
	if err != nil {
		return fmt.Errorf("find password field: %w", err)
	}
	if err := browser.TypeLikeHuman(passEl, cred.Password); err != nil {
		return fmt.Errorf("type password: %w", err)
	}

	browser.ShortDelay()

	// Click login button
	loginBtn, err := page.Timeout(actionTimeout).Element(`button[type="submit"]`)
	if err != nil {
		return fmt.Errorf("find login button: %w", err)
	}
	if err := loginBtn.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click login: %w", err)
	}

	// Wait for navigation
	browser.HumanDelay()
	browser.HumanDelay()

	// Check if login succeeded
	if err := c.verifyLogin(page); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	// Save session cookies
	if err := c.engine.SaveSession(platformName, page); err != nil {
		c.logger.Printf("[facebook] Warning: failed to save session: %v", err)
	}

	userName := c.extractUserName(page)

	c.mu.Lock()
	c.loggedIn = true
	c.userName = userName
	c.mu.Unlock()

	c.logger.Printf("[facebook] Login successful as %s", userName)

	c.store.UpsertAccount(ctx, &store.Account{
		Channel:    platformName,
		JID:        "fb:" + cred.Email,
		PushName:   userName,
		Status:     "connected",
		LastSeenAt: time.Now(),
	})

	return nil
}

// dismissCookieBanner tries to click the cookie accept button if present.
func (c *Connector) dismissCookieBanner(page *rod.Page) {
	selectors := []string{
		`[data-testid="cookie-policy-manage-dialog-accept-button"]`,
		`button[title="Allow all cookies"]`,
		`button[title="Allow essential and optional cookies"]`,
	}
	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			browser.ShortDelay()
			return
		}
	}
}

// verifyLogin checks if we ended up on a logged-in page.
func (c *Connector) verifyLogin(page *rod.Page) error {
	// Check for login error indicators
	errorSelectors := []string{
		`#error_box`,
		`[data-testid="login_error_message"]`,
	}
	for _, sel := range errorSelectors {
		el, err := page.Timeout(2 * time.Second).Element(sel)
		if err == nil {
			text, _ := el.Text()
			if text != "" {
				return fmt.Errorf("Facebook error: %s", strings.TrimSpace(text))
			}
		}
	}

	// Check for successful login indicators
	successSelectors := []string{
		`[aria-label="Facebook"]`,
		`div[role="navigation"]`,
		`[data-pagelet="LeftRail"]`,
	}
	for _, sel := range successSelectors {
		_, err := page.Timeout(10 * time.Second).Element(sel)
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("could not verify login — page may require 2FA or captcha")
}

// extractUserName tries to get the logged-in user's name from the page.
func (c *Connector) extractUserName(page *rod.Page) string {
	selectors := []string{
		`[aria-label="Your profile"] span`,
		`a[href*="/me/"] span`,
	}
	for _, sel := range selectors {
		el, err := page.Timeout(5 * time.Second).Element(sel)
		if err == nil {
			text, _ := el.Text()
			if text != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return "Facebook User"
}

// ---------------------------------------------------------------------------
// Posting
// ---------------------------------------------------------------------------

// CreatePost creates a text post on the user's timeline or a specific page.
func (c *Connector) CreatePost(ctx context.Context, content string, pageURL string) error {
	if !c.IsConnected() {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}

	if err := c.engine.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := facebookBaseURL
	if pageURL != "" {
		targetURL = pageURL
	}

	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return fmt.Errorf("navigate: %w", err)
	}

	browser.HumanDelay()

	// Click "What's on your mind?" area
	createPostSelectors := []string{
		`[aria-label="Create a post"]`,
		`div[role="button"] span`,
	}

	var postArea *rod.Element
	for _, sel := range createPostSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			postArea = el
			break
		}
	}

	if postArea == nil {
		return fmt.Errorf("could not find post creation area — selectors may have changed")
	}

	if err := postArea.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click create post: %w", err)
	}

	browser.HumanDelay()

	// Find the text input in the post dialog
	textInputSelectors := []string{
		`div[contenteditable="true"][role="textbox"]`,
		`[aria-label="What's on your mind?"]`,
	}

	var textInput *rod.Element
	for _, sel := range textInputSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			textInput = el
			break
		}
	}

	if textInput == nil {
		return fmt.Errorf("could not find post text input — dialog may not have opened")
	}

	if err := textInput.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click text input: %w", err)
	}
	browser.ShortDelay()

	if err := browser.TypeLikeHuman(textInput, content); err != nil {
		return fmt.Errorf("type post content: %w", err)
	}

	browser.HumanDelay()

	// Click Post button
	postBtnSelectors := []string{
		`[aria-label="Post"]`,
		`div[role="button"] span`,
	}

	for _, sel := range postBtnSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			text, _ := el.Text()
			if strings.TrimSpace(text) == "Post" {
				_ = el.Click(proto.InputMouseButtonLeft, 1)
				break
			}
		}
	}

	browser.HumanDelay()
	browser.HumanDelay()

	// Cache the post
	c.store.UpsertCachedPost(ctx, &store.CachedPost{
		Platform: platformName,
		PostID:   fmt.Sprintf("local_%d", time.Now().UnixMilli()),
		Content:  content,
		PostedAt: time.Now(),
	})

	c.logger.Printf("[facebook] Post created successfully")
	return nil
}

// ---------------------------------------------------------------------------
// Messenger
// ---------------------------------------------------------------------------

// ReadMessages returns cached messages, with cache metadata.
func (c *Connector) ReadMessages(ctx context.Context, conversationID string, limit int) ([]store.CachedMessage, time.Time, bool, error) {
	messages, lastSynced, err := c.store.GetCachedMessages(ctx, platformName, conversationID, limit)
	if err == nil && len(messages) > 0 {
		return messages, lastSynced, true, nil
	}
	return nil, time.Time{}, false, nil
}

// FetchFreshMessages opens Messenger and scrapes recent messages.
func (c *Connector) FetchFreshMessages(ctx context.Context, conversationID string) ([]store.CachedMessage, error) {
	if !c.IsConnected() {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}

	if err := c.engine.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := messengerURL
	if conversationID != "" {
		targetURL = messengerURL + "/t/" + conversationID
	}

	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return nil, fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()
	browser.HumanDelay()

	messages := c.scrapeMessages(page, conversationID)

	for i := range messages {
		c.store.UpsertCachedMessage(ctx, &messages[i])
	}

	c.logger.Printf("[facebook] Fetched %d messages", len(messages))
	return messages, nil
}

func (c *Connector) scrapeMessages(page *rod.Page, conversationID string) []store.CachedMessage {
	var messages []store.CachedMessage

	msgElements, err := page.Timeout(actionTimeout).Elements(`div[role="row"]`)
	if err != nil {
		c.logger.Printf("[facebook] Could not find message elements: %v", err)
		return messages
	}

	for i, el := range msgElements {
		text, err := el.Text()
		if err != nil || text == "" {
			continue
		}

		msg := store.CachedMessage{
			Platform:       platformName,
			ConversationID: conversationID,
			MessageID:      fmt.Sprintf("msg_%s_%d", conversationID, i),
			Content:        strings.TrimSpace(text),
			Timestamp:      time.Now(),
			IsOutgoing:     false,
		}
		messages = append(messages, msg)
	}

	return messages
}

// SendMessage sends a message in Facebook Messenger.
func (c *Connector) SendMessage(ctx context.Context, conversationID, message string) error {
	if !c.IsConnected() {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}

	if err := c.engine.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := messengerURL + "/t/" + conversationID
	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()

	// Find message input
	inputSelectors := []string{
		`div[aria-label="Message"][contenteditable="true"]`,
		`div[role="textbox"][contenteditable="true"]`,
	}

	var inputEl *rod.Element
	for _, sel := range inputSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			inputEl = el
			break
		}
	}

	if inputEl == nil {
		return fmt.Errorf("could not find message input — selectors may have changed")
	}

	if err := inputEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click input: %w", err)
	}
	browser.ShortDelay()

	if err := browser.TypeLikeHuman(inputEl, message); err != nil {
		return fmt.Errorf("type message: %w", err)
	}

	browser.ShortDelay()

	// Press Enter to send
	if err := inputEl.Type(input.Enter); err != nil {
		return fmt.Errorf("press enter: %w", err)
	}

	browser.HumanDelay()

	c.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       platformName,
		ConversationID: conversationID,
		MessageID:      fmt.Sprintf("sent_%d", time.Now().UnixMilli()),
		Content:        message,
		Timestamp:      time.Now(),
		IsOutgoing:     true,
	})

	c.logger.Printf("[facebook] Message sent to conversation %s", conversationID)
	return nil
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

// GetContacts returns cached contacts.
func (c *Connector) GetContacts(ctx context.Context, query string, limit int) ([]store.CachedContact, time.Time, bool, error) {
	contacts, lastSynced, err := c.store.GetCachedContacts(ctx, platformName, query, limit)
	if err == nil && len(contacts) > 0 {
		return contacts, lastSynced, true, nil
	}
	return nil, time.Time{}, false, nil
}

// FetchFreshContacts opens Messenger and scrapes the contact list.
func (c *Connector) FetchFreshContacts(ctx context.Context) ([]store.CachedContact, error) {
	if !c.IsConnected() {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}

	if err := c.engine.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	if err := c.engine.NavigateAndWait(page, messengerURL, actionTimeout); err != nil {
		return nil, fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()
	browser.HumanDelay()

	var contacts []store.CachedContact
	convElements, err := page.Timeout(actionTimeout).Elements(`a[href*="/t/"]`)
	if err != nil {
		return nil, fmt.Errorf("find conversations: %w", err)
	}

	for _, el := range convElements {
		name, err := el.Text()
		if err != nil || name == "" {
			continue
		}

		href, err := el.Attribute("href")
		if err != nil || href == nil {
			continue
		}

		parts := strings.Split(*href, "/t/")
		if len(parts) < 2 {
			continue
		}
		convID := strings.TrimRight(parts[1], "/")
		if convID == "" {
			continue
		}

		contact := store.CachedContact{
			Platform:   platformName,
			ContactID:  convID,
			Name:       strings.TrimSpace(name),
			ProfileURL: messengerURL + "/t/" + convID,
		}
		contacts = append(contacts, contact)
		c.store.UpsertCachedContact(ctx, &contact)
	}

	c.logger.Printf("[facebook] Fetched %d contacts", len(contacts))
	return contacts, nil
}

// ---------------------------------------------------------------------------
// Health Check
// ---------------------------------------------------------------------------

// HealthCheck verifies the connector is working by checking key selectors.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if err := c.engine.Start(); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("browser failed: %w", err))
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("page creation failed: %w", err))
	}
	defer page.Close()

	// Check 1: Login page loads and has expected selectors
	if err := c.engine.NavigateAndWait(page, loginURL, healthTimeout); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("cannot load login page: %w", err))
	}

	if _, err := page.Timeout(healthTimeout).Element("#email"); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("login #email not found — Facebook may have changed"))
	}

	if _, err := page.Timeout(healthTimeout).Element("#pass"); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("login #pass not found — Facebook may have changed"))
	}

	// Check 2: If logged in, verify Messenger loads
	if c.IsConnected() {
		if err := c.engine.NavigateAndWait(page, messengerURL, healthTimeout); err != nil {
			return c.saveHealthCheck(ctx, fmt.Errorf("cannot load Messenger: %w", err))
		}
	}

	// All good
	c.store.SaveHealthCheck(ctx, &store.HealthCheckResult{
		Platform:  platformName,
		Status:    "ok",
		CheckedAt: time.Now(),
	})
	c.logger.Printf("[facebook] Health check passed")
	return nil
}

func (c *Connector) saveHealthCheck(ctx context.Context, err error) error {
	c.store.SaveHealthCheck(ctx, &store.HealthCheckResult{
		Platform:  platformName,
		Status:    "failed",
		Error:     err.Error(),
		CheckedAt: time.Now(),
	})
	c.logger.Printf("[facebook] Health check FAILED: %v", err)
	return err
}

// Disconnect marks the connector as disconnected.
func (c *Connector) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loggedIn = false
	c.userName = ""
	c.pageInfo = nil
}
