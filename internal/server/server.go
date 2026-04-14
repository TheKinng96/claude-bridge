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
	"time"

	"context"

	"claude-bridge/internal/batch"
	"claude-bridge/internal/browser"
	"claude-bridge/internal/connectors/facebook"
	"claude-bridge/internal/connectors/whatsapp"
	"claude-bridge/internal/mcp"
	"claude-bridge/internal/store"
)

// Server is the HTTP server that serves the UI and API.
type Server struct {
	wa            *whatsapp.Manager
	fb            *facebook.Connector
	store         *store.Store
	browserEngine *browser.Engine
	batchQueue    *batch.Queue
	port          int
	listener      net.Listener
	tlsListener   net.Listener
	mu            sync.Mutex
}

// New creates a new server. Pass the connectors so the API can interact with them.
func New(wa *whatsapp.Manager, fb *facebook.Connector, appStore *store.Store, browserEngine *browser.Engine, port int) *Server {
	s := &Server{wa: wa, fb: fb, store: appStore, browserEngine: browserEngine, port: port}
	s.batchQueue = batch.NewQueue(s.executeBatchJob)
	return s
}

// executeBatchJob is the callback the batch queue uses to run each job.
func (s *Server) executeBatchJob(ctx context.Context, jobType batch.JobType, platform string, params map[string]string) error {
	switch platform {
	case "facebook":
		switch jobType {
		case batch.JobCreatePost:
			return s.fb.CreatePost(ctx, params["content"], params["page_url"])
		case batch.JobSendMessage:
			return s.fb.Messenger.SendMessage(ctx, params["recipient_id"], params["message"])
		case batch.JobReplyComment:
			return s.fb.Messenger.ReplyToComment(ctx, params["comment_id"], params["message"])
		}
	}
	return fmt.Errorf("unsupported: %s/%s", platform, jobType)
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

	// API — Facebook (browser automation for posting)
	mux.HandleFunc("/api/facebook/login", s.handleFBLogin)
	mux.HandleFunc("/api/facebook/login/status", s.handleFBLoginStatus)
	mux.HandleFunc("/api/facebook/login/confirm", s.handleFBLoginConfirm)
	mux.HandleFunc("/api/facebook/disconnect", s.handleFBDisconnect)
	mux.HandleFunc("/api/facebook/status", s.handleFBStatus)
	mux.HandleFunc("/api/facebook/post", s.handleFBCreatePost)
	mux.HandleFunc("/api/facebook/healthcheck", s.handleFBHealthCheck)

	// API — Facebook Messenger (Graph API)
	mux.HandleFunc("/api/facebook/messenger/oauth/config", s.handleFBMessengerOAuthConfig)
	mux.HandleFunc("/api/facebook/messenger/oauth/start", s.handleFBMessengerOAuthStart)
	mux.HandleFunc("/api/facebook/messenger/oauth/callback", s.handleFBMessengerOAuthCallback)
	mux.HandleFunc("/api/facebook/messenger/status", s.handleFBMessengerStatus)
	mux.HandleFunc("/api/facebook/messenger/disconnect", s.handleFBMessengerDisconnect)
	mux.HandleFunc("/api/facebook/messenger/conversations", s.handleFBMessengerConversations)
	mux.HandleFunc("/api/facebook/messenger/messages", s.handleFBMessengerMessages)
	mux.HandleFunc("/api/facebook/messenger/send", s.handleFBMessengerSend)
	mux.HandleFunc("/api/facebook/messenger/polling", s.handleFBMessengerPolling)
	mux.HandleFunc("/api/facebook/messenger/posts", s.handleFBMessengerPosts)
	mux.HandleFunc("/api/facebook/messenger/comments", s.handleFBMessengerComments)
	mux.HandleFunc("/api/facebook/messenger/comments/reply", s.handleFBMessengerReplyComment)
	mux.HandleFunc("/api/facebook/messenger/analytics", s.handleFBMessengerAnalytics)

	// API — Batch queue
	mux.HandleFunc("/api/batch/submit", s.handleBatchSubmit)
	mux.HandleFunc("/api/batch/status", s.handleBatchStatus)
	mux.HandleFunc("/api/batch/list", s.handleBatchList)
	mux.HandleFunc("/api/batch/cancel", s.handleBatchCancel)

	// OAuth callback (served on HTTPS for Facebook redirect)
	mux.HandleFunc("/callback", s.handleFBMessengerOAuthCallback)

	// API — Browser engine
	mux.HandleFunc("/api/browser/status", s.handleBrowserStatus)
	mux.HandleFunc("/api/browser/setup", s.handleBrowserSetup)
	mux.HandleFunc("/api/browser/headless", s.handleBrowserHeadless)

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
		log.Printf("Claude Bridge HTTP  server running at http://%s", addr)
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
	Connected  bool   `json:"connected"`
	UserName   string `json:"user_name,omitempty"`
	ProfilePic string `json:"profile_pic,omitempty"`
	PageName   string `json:"page_name,omitempty"`
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
			Connected:  s.fb.IsConnected(),
			UserName:   s.fb.UserName(),
			ProfilePic: s.fb.ProfilePic(),
			PageName:   s.fb.PageName(),
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

// handleFBLogin opens a visible browser for the user to log in to Facebook.
func (s *Server) handleFBLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if err := s.fb.OpenLoginBrowser(r.Context()); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "message": "Browser opened — please log in to Facebook"})
}

// handleFBLoginStatus returns the current login flow status.
func (s *Server) handleFBLoginStatus(w http.ResponseWriter, r *http.Request) {
	loginStatus := s.fb.LoginState()
	cs := s.fb.Status()
	writeJSON(w, map[string]interface{}{
		"ok":          true,
		"state":       loginStatus.State,
		"message":     loginStatus.Message,
		"connected":   cs.Connected,
		"user_name":   cs.UserName,
		"profile_pic": cs.ProfilePic,
	})
}

// handleFBLoginConfirm is called when the user clicks "I have logged in".
// This is synchronous — it verifies login and returns the result.
func (s *Server) handleFBLoginConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	ok, message := s.fb.ConfirmLogin(r.Context())
	writeJSON(w, map[string]interface{}{
		"ok":      ok,
		"message": message,
	})
}

// handleFBDisconnect disconnects Facebook.
func (s *Server) handleFBDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.fb.Disconnect()
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleFBStatus returns Facebook connection status.
func (s *Server) handleFBStatus(w http.ResponseWriter, r *http.Request) {
	cs := s.fb.Status()
	writeJSON(w, map[string]interface{}{
		"connected":   cs.Connected,
		"user_name":   cs.UserName,
		"profile_pic": cs.ProfilePic,
		"page_name":   cs.PageName,
	})
}

// --- Facebook Messenger API handlers ---

func (s *Server) handleFBMessengerOAuthConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		oauth := s.fb.Messenger.GetOAuthConfig()
		if oauth == nil {
			writeJSON(w, map[string]interface{}{"ok": true, "configured": false})
			return
		}
		writeJSON(w, map[string]interface{}{
			"ok":         true,
			"configured": true,
			"app_id":     oauth.AppID,
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.AppID == "" || body.AppSecret == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "app_id and app_secret required"})
		return
	}
	redirectURI := fmt.Sprintf("https://127.0.0.1:%d/callback", s.tlsPort())
	if err := s.fb.Messenger.SetOAuthConfig(r.Context(), body.AppID, body.AppSecret, redirectURI); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "redirect_uri": redirectURI})
}

func (s *Server) handleFBMessengerOAuthStart(w http.ResponseWriter, r *http.Request) {
	state := fmt.Sprintf("%d", time.Now().UnixNano())
	oauthURL, err := s.fb.Messenger.GetOAuthURL(state)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "url": oauthURL, "state": state})
}

func (s *Server) handleFBMessengerOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		errMsg := r.URL.Query().Get("error_description")
		if errMsg == "" {
			errMsg = "No authorization code received"
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h2>Authorization Failed</h2><p>%s</p><p>You can close this tab.</p></body></html>`, errMsg)
		return
	}

	if err := s.fb.Messenger.HandleOAuthCallback(r.Context(), code); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h2>Connection Failed</h2><p>%s</p><p>Please try again from the dashboard.</p></body></html>`, err.Error())
		return
	}

	// Start polling after successful OAuth
	s.fb.Messenger.StartPolling(r.Context())

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<html><body><h2>Connected!</h2><p>Facebook Messenger is now connected. You can close this tab and return to the dashboard.</p><script>setTimeout(function(){window.close();},3000);</script></body></html>`)
}

func (s *Server) handleFBMessengerStatus(w http.ResponseWriter, r *http.Request) {
	connected := s.fb.Messenger.IsConnected()
	resp := map[string]interface{}{
		"connected": connected,
		"polling":   s.fb.Messenger.IsPolling(),
	}
	if connected {
		pt := s.fb.Messenger.GetPageInfo()
		if pt != nil {
			resp["page_id"] = pt.PageID
			resp["page_name"] = pt.PageName
		}
	}
	writeJSON(w, resp)
}

func (s *Server) handleFBMessengerDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	s.fb.Messenger.Disconnect(r.Context())
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleFBMessengerConversations(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	refresh := r.URL.Query().Get("refresh") == "true"

	if !refresh {
		// Try cached first
		contacts, lastSynced, err := s.store.GetCachedContacts(r.Context(), "facebook", "", limit)
		if err == nil && len(contacts) > 0 {
			writeJSON(w, map[string]interface{}{
				"ok":           true,
				"conversations": contacts,
				"cached":       true,
				"synced_at":    lastSynced,
				"total":        len(contacts),
			})
			return
		}
	}

	conversations, err := s.fb.Messenger.GetConversations(r.Context(), limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"conversations": conversations,
		"cached":       false,
		"synced_at":    time.Now(),
		"total":        len(conversations),
	})
}

func (s *Server) handleFBMessengerMessages(w http.ResponseWriter, r *http.Request) {
	conversationID := r.URL.Query().Get("conversation")
	if conversationID == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "conversation parameter required"})
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	refresh := r.URL.Query().Get("refresh") == "true"

	if !refresh {
		msgs, lastSynced, err := s.store.GetCachedMessages(r.Context(), "facebook", conversationID, limit)
		if err == nil && len(msgs) > 0 {
			writeJSON(w, map[string]interface{}{
				"ok":        true,
				"messages":  msgs,
				"cached":    true,
				"synced_at": lastSynced,
			})
			return
		}
	}

	msgs, err := s.fb.Messenger.GetMessages(r.Context(), conversationID, limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":        true,
		"messages":  msgs,
		"cached":    false,
		"synced_at": time.Now(),
	})
}

func (s *Server) handleFBMessengerSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RecipientID string `json:"recipient_id"`
		Message     string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.RecipientID == "" || body.Message == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "recipient_id and message required"})
		return
	}
	if err := s.fb.Messenger.SendMessage(r.Context(), body.RecipientID, body.Message); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

func (s *Server) handleFBMessengerPolling(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]interface{}{"polling": s.fb.Messenger.IsPolling()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Enabled {
		s.fb.Messenger.StartPolling(r.Context())
	} else {
		s.fb.Messenger.StopPolling()
	}
	writeJSON(w, map[string]interface{}{"ok": true, "polling": body.Enabled})
}

func (s *Server) handleFBMessengerPosts(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	refresh := r.URL.Query().Get("refresh") == "true"

	if !refresh {
		posts, lastSynced, err := s.store.GetCachedPosts(r.Context(), "facebook", limit)
		if err == nil && len(posts) > 0 {
			writeJSON(w, map[string]interface{}{
				"ok":        true,
				"posts":     posts,
				"cached":    true,
				"synced_at": lastSynced,
				"total":     len(posts),
			})
			return
		}
	}

	posts, err := s.fb.Messenger.GetPosts(r.Context(), limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":        true,
		"posts":     posts,
		"cached":    false,
		"synced_at": time.Now(),
		"total":     len(posts),
	})
}

func (s *Server) handleFBMessengerComments(w http.ResponseWriter, r *http.Request) {
	postID := r.URL.Query().Get("post_id")
	if postID == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "post_id parameter required"})
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	refresh := r.URL.Query().Get("refresh") == "true"

	if !refresh {
		comments, lastSynced, err := s.store.GetCachedComments(r.Context(), "facebook", postID, limit)
		if err == nil && len(comments) > 0 {
			writeJSON(w, map[string]interface{}{
				"ok":        true,
				"comments":  comments,
				"cached":    true,
				"synced_at": lastSynced,
				"total":     len(comments),
			})
			return
		}
	}

	comments, err := s.fb.Messenger.GetComments(r.Context(), postID, limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":        true,
		"comments":  comments,
		"cached":    false,
		"synced_at": time.Now(),
		"total":     len(comments),
	})
}

func (s *Server) handleFBMessengerReplyComment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		CommentID string `json:"comment_id"`
		Message   string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.CommentID == "" || body.Message == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "comment_id and message required"})
		return
	}
	if err := s.fb.Messenger.ReplyToComment(r.Context(), body.CommentID, body.Message); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleFBMessengerAnalytics returns Page-level analytics/insights.
func (s *Server) handleFBMessengerAnalytics(w http.ResponseWriter, r *http.Request) {
	// Fetch posts and compute aggregate stats
	posts, lastSynced, err := s.store.GetCachedPosts(r.Context(), "facebook", 100)
	if err != nil || len(posts) == 0 {
		// Try fetching fresh
		posts, err = s.fb.Messenger.GetPosts(r.Context(), 100)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		lastSynced = time.Now()
	}

	totalLikes := 0
	totalComments := 0
	totalShares := 0
	for _, p := range posts {
		totalLikes += p.Likes
		totalComments += p.Comments
		totalShares += p.Shares
	}

	avgEngagement := 0.0
	if len(posts) > 0 {
		avgEngagement = float64(totalLikes+totalComments+totalShares) / float64(len(posts))
	}

	writeJSON(w, map[string]interface{}{
		"ok":              true,
		"total_posts":     len(posts),
		"total_likes":     totalLikes,
		"total_comments":  totalComments,
		"total_shares":    totalShares,
		"avg_engagement":  avgEngagement,
		"synced_at":       lastSynced,
	})
}

// --- Batch queue handlers ---

func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Platform string              `json:"platform"`
		Type     string              `json:"type"`
		Items    []map[string]string `json:"items"`
		MinDelay int                 `json:"min_delay_seconds"`
		MaxDelay int                 `json:"max_delay_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Platform == "" || body.Type == "" || len(body.Items) == 0 {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "platform, type, and items are required"})
		return
	}

	batchID := s.batchQueue.Submit(body.Platform, batch.JobType(body.Type), body.Items, body.MinDelay, body.MaxDelay)
	writeJSON(w, map[string]interface{}{
		"ok":       true,
		"batch_id": batchID,
		"total":    len(body.Items),
		"message":  fmt.Sprintf("Batch submitted: %d %s jobs queued with %d-%ds delay between each", len(body.Items), body.Type, body.MinDelay, body.MaxDelay),
	})
}

func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	batchID := r.URL.Query().Get("batch_id")
	if batchID == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "batch_id parameter required"})
		return
	}
	b := s.batchQueue.GetBatch(batchID)
	if b == nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "batch not found"})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "batch": b})
}

func (s *Server) handleBatchList(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	batches := s.batchQueue.ListBatches(platform)
	writeJSON(w, map[string]interface{}{"ok": true, "batches": batches, "total": len(batches)})
}

func (s *Server) handleBatchCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		BatchID string `json:"batch_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.BatchID == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "batch_id required"})
		return
	}
	if s.batchQueue.CancelBatch(body.BatchID) {
		writeJSON(w, map[string]interface{}{"ok": true, "message": "Batch cancelled"})
	} else {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "batch not found or already finished"})
	}
}

// tlsPort returns the TLS port from the listener.
func (s *Server) tlsPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tlsListener != nil {
		addr := s.tlsListener.Addr().String()
		parts := strings.Split(addr, ":")
		if len(parts) > 0 {
			if p, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
				return p
			}
		}
	}
	return 10003 // default
}

// handleFBCreatePost creates a Facebook post.
func (s *Server) handleFBCreatePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Content string `json:"content"`
		PageURL string `json:"page_url,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Content == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "content required"})
		return
	}
	log.Printf("[api] handleFBCreatePost: starting browser automation (ctx alive: %v)", r.Context().Err() == nil)
	err := s.fb.CreatePost(r.Context(), body.Content, body.PageURL)
	log.Printf("[api] handleFBCreatePost: CreatePost returned (err=%v, ctx alive: %v)", err, r.Context().Err() == nil)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
	log.Printf("[api] handleFBCreatePost: response written ok=true")
}

// handleFBHealthCheck runs the Facebook health check.
func (s *Server) handleFBHealthCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.fb.HealthCheck(r.Context()); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleBrowserStatus returns the current browser engine setup status.
func (s *Server) handleBrowserStatus(w http.ResponseWriter, r *http.Request) {
	status := s.browserEngine.GetSetupStatus()
	writeJSON(w, status)
}

// handleBrowserSetup triggers browser setup check (finds Chrome or downloads Chromium).
func (s *Server) handleBrowserSetup(w http.ResponseWriter, r *http.Request) {
	status := s.browserEngine.CheckAndSetup()
	writeJSON(w, status)
}

// handleBrowserHeadless toggles headless mode for the browser engine.
func (s *Server) handleBrowserHeadless(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]interface{}{
			"headless": s.browserEngine.IsHeadless(),
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Headless bool `json:"headless"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	s.browserEngine.SetHeadless(body.Headless)
	writeJSON(w, map[string]interface{}{"ok": true, "headless": body.Headless})
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
