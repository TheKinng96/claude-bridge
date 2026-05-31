package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/batch"
	"claude-bridge/internal/broadcast"
	"claude-bridge/internal/browser"
	"claude-bridge/internal/claude"
	"claude-bridge/internal/connectors/facebook"
	"claude-bridge/internal/connectors/telegram"
	"claude-bridge/internal/connectors/whatsapp"
	"claude-bridge/internal/cowork"
	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/mcp"
	"claude-bridge/internal/ownerprofile"
	"claude-bridge/internal/store"
)

//go:embed assets
var staticAssets embed.FS

// Server is the HTTP server that serves the UI and API.
type Server struct {
	wa            *whatsapp.Manager
	fb            *facebook.Connector
	store         *store.Store
	browserEngine *browser.Engine
	batchQueue    *batch.Queue
	knowClient    *claude.Client
	knowPipeline  *knowledge.Pipeline
	knowWatcher   *knowledge.Watcher
	knowEmbedder  *knowledge.Embedder // nil if Ollama not available
	cowork        *cowork.Root        // nil-safe via Enabled(); cowork output folder
	ownerProfile  *ownerprofile.Store // nil-safe via Enabled(); shared profile note
	agentRunner   agentRunner
	tg            *telegram.Client // nil if not configured
	tgReloader    func() error     // optional: called after agent config POST to live-reload Telegram
	dispatchProbe DispatchProbeFunc // optional: handler for /api/dispatch/test
	updateReady   bool
	port          int
	listener      net.Listener
	tlsListener   net.Listener
	mu            sync.Mutex
	sessions      *sessionStore

	dailySendCount   map[string]int // key = YYYY-MM-DD; in-memory, resets on restart
	dailySendCountMu sync.Mutex
}

// agentRunner is a minimal interface so server.go doesn't import the agent package.
type agentRunner interface{}

// SetAgent attaches the auto-reply agent runner.
func (s *Server) SetAgent(r agentRunner) { s.agentRunner = r }

// SetTelegram attaches the Telegram connector so HTTP handlers (settings UI,
// test-connection endpoint, dispatch loop) can reach it.
func (s *Server) SetTelegram(c *telegram.Client) { s.tg = c }

// SetTelegramReloader registers a callback the config save handler invokes
// after writing new Telegram fields so the long-poll restarts with fresh
// token + allowlist instead of requiring a process restart.
func (s *Server) SetTelegramReloader(f func() error) { s.tgReloader = f }

// DispatchProbeReply is what the /api/dispatch/test endpoint surfaces per
// attempt. Plain strings keep the server package free of an agent dependency.
type DispatchProbeReply struct {
	Action    string `json:"action"`
	UserReply string `json:"user_reply"`
	Error     string `json:"error,omitempty"`
}

// DispatchProbeFunc runs ONE dispatcher turn for the test endpoint. main.go
// wires this to dispatcher.Run via a thin adapter so server.go avoids
// importing the agent package.
type DispatchProbeFunc func(ctx context.Context, channel, ownerID, message string) DispatchProbeReply

// SetDispatchProbe registers the dispatcher-run callback for /api/dispatch/test.
func (s *Server) SetDispatchProbe(f DispatchProbeFunc) { s.dispatchProbe = f }

// BatchQueue exposes the batch queue so other subsystems (dispatch executor)
// can submit jobs directly without going through the HTTP layer.
func (s *Server) BatchQueue() *batch.Queue { return s.batchQueue }

// profileSnapshotFromStore flattens a store.ClientProfile into the broadcast
// package's ProfileSnapshot so broadcast doesn't have to import store.
func profileSnapshotFromStore(p *store.ClientProfile) *broadcast.ProfileSnapshot {
	if p == nil {
		return nil
	}
	return &broadcast.ProfileSnapshot{
		Role:        p.Role,
		Language:    p.Language,
		FamilyNotes: p.FamilyNotes,
		Interests:   p.Interests,
		LastTopics:  p.LastTopics,
		CustomNotes: p.CustomNotes,
	}
}

// New creates a new server. Pass the connectors so the API can interact with them.
func New(wa *whatsapp.Manager, fb *facebook.Connector, appStore *store.Store, browserEngine *browser.Engine, port int) *Server {
	s := &Server{
		wa:             wa,
		fb:             fb,
		store:          appStore,
		browserEngine:  browserEngine,
		port:           port,
		sessions:       newSessionStore(),
		dailySendCount: make(map[string]int),
	}
	s.batchQueue = batch.NewQueue(s.executeBatchJob)
	return s
}

// SetKnowledge attaches the knowledge subsystem. main.go calls this after
// wiring up the shared claude.Client, pipeline, and watcher.
func (s *Server) SetKnowledge(c *claude.Client, p *knowledge.Pipeline, w *knowledge.Watcher) {
	s.knowClient = c
	s.knowPipeline = p
	s.knowWatcher = w
}

// SetEmbedder attaches the Ollama embedder (nil = disabled).
func (s *Server) SetEmbedder(e *knowledge.Embedder) {
	s.knowEmbedder = e
}

// SetCowork attaches the cowork output-folder root. Used by the
// /api/cowork/folder endpoint (and the get_cowork_folder MCP tool) so external
// routines can learn where to write today's files.
func (s *Server) SetCowork(c *cowork.Root) {
	s.cowork = c
}

// SetOwnerProfile attaches the owner-profile store. Used by the
// /api/owner-profile endpoint (and the get_owner_profile / update_owner_profile
// MCP tools) so the Cowork app can read and update the shared profile.
func (s *Server) SetOwnerProfile(st *ownerprofile.Store) {
	s.ownerProfile = st
}

// SetUpdateReady signals that a new binary has been downloaded and is ready to apply.
func (s *Server) SetUpdateReady() {
	s.mu.Lock()
	s.updateReady = true
	s.mu.Unlock()
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ready := s.updateReady
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ready": ready})
}

func (s *Server) handleUpdateRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		// Give the response time to flush before exiting.
		// launchd KeepAlive will restart the process with the new binary.
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
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
	case "whatsapp":
		switch jobType {
		case batch.JobSendMessage:
			return s.executeWhatsAppSend(ctx, params)
		}
	}
	return fmt.Errorf("unsupported: %s/%s", platform, jobType)
}

// executeWhatsAppSend handles a single WhatsApp send job, optionally personalizing
// the message via Claude before delivering it through the WhatsApp connector.
//
// Required params:   phone, message
// Optional params:   from_jid, contact_name, personalize ("true"|"false"),
//
//	instructions, recent_msgs (newline-joined inbound history;
//	individual messages must not contain embedded newlines)
func (s *Server) executeWhatsAppSend(ctx context.Context, params map[string]string) error {
	phone := params["phone"]
	if phone == "" {
		return fmt.Errorf("missing phone")
	}

	text := params["message"]
	contactName := params["contact_name"]
	if contactName == "" {
		contactName = phone
	}

	// Render template placeholders for the non-personalized path.
	// When personalize=true, Generate() does its own rendering on the raw template,
	// so we pass params["message"] there to avoid a double-render.
	text = broadcast.Render(text, map[string]string{
		"name":      contactName,
		"push_name": contactName,
	})

	// s.knowClient is reused for broadcast personalization — the server has no
	// separate "agent" client; the knowledge subsystem's client is equivalent here.
	if params["personalize"] == "true" && s.knowClient != nil {
		var recent []string
		if rm := params["recent_msgs"]; rm != "" {
			recent = strings.Split(rm, "\n")
		} else if s.store != nil {
			// Auto-fetch recent inbound messages so Claude has context for personalization.
			// 5 recent messages are enough — more would inflate prompts without much benefit.
			if msgs, err := s.store.RecentInboundMessages(ctx, phone, 5); err == nil {
				for _, m := range msgs {
					// Inbound bodies may contain newlines; collapse them so individual messages
					// render on single lines in the personalizer's prompt.
					recent = append(recent, strings.ReplaceAll(m, "\n", " "))
				}
			}
		}
		// Pull client_profile if one exists. Phone is just the number; JID adds
		// the whatsapp.net suffix. Profile lookup tolerates either by passing
		// the phone through GetClientProfile and FindClientProfileByName.
		var snap *broadcast.ProfileSnapshot
		if s.store != nil {
			jid := phone + "@s.whatsapp.net"
			if cp, _ := s.store.GetClientProfile(ctx, jid); cp != nil {
				snap = profileSnapshotFromStore(cp)
			}
		}

		p := broadcast.Personalizer{Claude: s.knowClient}
		// Generate never returns a non-nil error today (it logs and falls back to
		// the rendered template), but check err defensively for future changes.
		generated, err := p.Generate(ctx, broadcast.Input{
			ContactName:    contactName,
			BaseTemplate:   params["message"], // pass raw template; Generate renders
			Instructions:   params["instructions"],
			RecentMessages: recent,
			Profile:        snap,
		})
		if err == nil && generated != "" {
			text = generated
		}
	}

	return s.wa.SendMessage(phone, text, params["from_jid"])
}

// buildMux creates the HTTP route mux. Shared between HTTP and HTTPS servers.
func (s *Server) buildMux() http.Handler {
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

	// Embedded binary assets (images, etc) under /static/assets/...
	assetsSub, _ := fs.Sub(staticAssets, "assets")
	mux.Handle("/static/assets/", http.StripPrefix("/static/assets/", http.FileServer(http.FS(assetsSub))))

	// Pages
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/setup/whatsapp", s.handleWhatsApp)
	mux.HandleFunc("/setup/whatsapp-business", s.handleWhatsAppBusiness)
	mux.HandleFunc("/setup/facebook", s.handleFacebook)
	mux.HandleFunc("/setup/knowledge", s.handleKnowledge)

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
	mux.HandleFunc("/api/batch/events", s.handleBatchEvents)

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

	// API — Knowledge ingestion
	mux.HandleFunc("/setup/agent", s.handleAgent)
	mux.HandleFunc("/api/agent/config", s.handleAgentConfig)
	mux.HandleFunc("/api/agent/replies", s.handleAgentReplies)
	mux.HandleFunc("/api/telegram/test", s.handleTelegramTest)

	// Contacts + Groups
	mux.HandleFunc("/contacts", s.handleContactsPage)
	mux.HandleFunc("/api/contacts", s.handleListContacts)
	mux.HandleFunc("/api/contacts/", s.handleContactsSubroute)
	mux.HandleFunc("/api/groups", s.handleGroupsRoute)
	mux.HandleFunc("/api/groups/", s.handleGroupsSubroute)

	// Broadcast progress page
	mux.HandleFunc("/broadcasts/", s.handleBroadcastProgress)

	// Messages (pending replies)
	mux.HandleFunc("/messages", s.handleMessagesPage)
	mux.HandleFunc("/api/pending-replies", s.handleListPendingReplies)
	mux.HandleFunc("/api/pending-replies/", s.handlePendingRepliesSubroute)

	mux.HandleFunc("/api/knowledge/config", s.handleKnowledgeConfig)
	mux.HandleFunc("/api/browse-folder", s.handleBrowseFolder)
	mux.HandleFunc("/api/documents", s.handleDocumentsList)
	mux.HandleFunc("/api/documents/", s.handleDocumentByID) // /api/documents/{id} and /api/documents/{id}/reingest
	mux.HandleFunc("/api/documents/search", s.handleDocumentsSearch)
	mux.HandleFunc("/api/documents/rescan", s.handleDocumentsRescan)
	mux.HandleFunc("/api/documents/unindexed-count", s.handleDocumentsUnindexedCount)
	mux.HandleFunc("/api/cowork/folder", s.handleCoworkFolder)
	mux.HandleFunc("/api/dispatch/test", s.handleDispatchTest)
	mux.HandleFunc("/api/owner-profile", s.handleOwnerProfile)

	// Self-update status + restart
	mux.HandleFunc("/api/update/status", s.handleUpdateStatus)
	mux.HandleFunc("/api/update/restart", s.handleUpdateRestart)

	// MCP SSE endpoints — available for remote MCP clients
	mcpHandler := mcp.NewSSEHandler(fmt.Sprintf("http://127.0.0.1:%d", s.port))
	mux.HandleFunc("/mcp/sse", mcpHandler.HandleSSE)
	mux.HandleFunc("/mcp/message", mcpHandler.HandleMessage)

	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/login", s.handleLogin)
	return s.sessionMiddleware(mux)
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

// handleCoworkFolder returns (and creates) the cowork output folder for a
// given date. External routines call this — directly or via the
// get_cowork_folder MCP tool — to learn where to write so the file is the
// same one the Telegram dispatcher lists/reads/edits. Query: ?date=today |
// yesterday | YYYY-MM-DD (default today).
func (s *Server) handleCoworkFolder(w http.ResponseWriter, r *http.Request) {
	if s.cowork == nil || !s.cowork.Enabled() {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "cowork disabled — set an Obsidian vault path in the agent config first"})
		return
	}
	dir, err := s.cowork.EnsureDate(r.URL.Query().Get("date"))
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "path": dir})
}

// handleDispatchTest exercises the dispatcher with a probe message and retries
// until the reply looks healthy (no parse-fallback line, no "not found"-style
// give-up). Used for iterative tuning of the system prompt without going
// through Telegram.
//
// POST body:
//
//	{
//	  "message":  "i put a pdf in the knowledge base, can you read it?",  // default
//	  "retries":  3,            // default 3, max 8
//	  "channel":  "probe",      // default "probe"
//	  "owner_id": "probe"       // default "probe"
//	}
//
// Returns: {ok, attempts: [...DispatchProbeReply], healthy: bool, final: DispatchProbeReply}.
func (s *Server) handleDispatchTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.dispatchProbe == nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "dispatcher not wired"})
		return
	}
	body := struct {
		Message string `json:"message"`
		Retries int    `json:"retries"`
		Channel string `json:"channel"`
		OwnerID string `json:"owner_id"`
	}{}
	if r.Method == http.MethodPost {
		// Tolerate empty bodies — defaults below cover everything.
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Message == "" {
		body.Message = "i put a pdf in the knowledge base, can you read it?"
	}
	if body.Channel == "" {
		body.Channel = "probe"
	}
	if body.OwnerID == "" {
		body.OwnerID = "probe"
	}
	if body.Retries <= 0 {
		body.Retries = 3
	}
	if body.Retries > 8 {
		body.Retries = 8
	}

	attempts := make([]DispatchProbeReply, 0, body.Retries)
	var last DispatchProbeReply
	healthy := false
	for i := 0; i < body.Retries; i++ {
		last = s.dispatchProbe(r.Context(), body.Channel, body.OwnerID, body.Message)
		attempts = append(attempts, last)
		if dispatchProbeHealthy(last) {
			healthy = true
			break
		}
	}
	writeJSON(w, map[string]interface{}{
		"ok":       true,
		"healthy":  healthy,
		"attempts": attempts,
		"final":    last,
	})
}

// dispatchProbeHealthy is the loop's "did we get a useful answer?" check.
// Tweak the markers list as new give-up phrasings surface.
func dispatchProbeHealthy(r DispatchProbeReply) bool {
	if r.Error != "" {
		return false
	}
	low := strings.ToLower(r.UserReply)
	for _, bad := range []string{
		"not found",
		"no pdf",
		"didn't find",
		"couldn't find",
		"could not find",
		"sideways on my end",
		"dispatch failed",
		"i'm not sure what",
	} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	return true
}

// handleOwnerProfile reads (GET) or writes (POST) the shared owner profile.
// POST body: {"content": "...", "mode": "replace"|"append"}.
func (s *Server) handleOwnerProfile(w http.ResponseWriter, r *http.Request) {
	if s.ownerProfile == nil || !s.ownerProfile.Enabled() {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "owner profile disabled — set an Obsidian vault path (or knowledge folder) first"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		content, err := s.ownerProfile.Read()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "content": content})
	case http.MethodPost:
		var body struct {
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "bad request body"})
			return
		}
		if strings.TrimSpace(body.Content) == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "content required"})
			return
		}
		if err := s.ownerProfile.Write(body.Content, body.Mode); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWAAccounts returns the list of all WhatsApp accounts.
func (s *Server) handleWAAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		s.handleWARemoveAccount(w, r)
		return
	}
	accounts := s.wa.GetAccounts()
	writeJSON(w, map[string]interface{}{
		"accounts":  accounts,
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
				"ok":            true,
				"conversations": contacts,
				"cached":        true,
				"synced_at":     lastSynced,
				"total":         len(contacts),
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
		"ok":            true,
		"conversations": conversations,
		"cached":        false,
		"synced_at":     time.Now(),
		"total":         len(conversations),
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
		"ok":             true,
		"total_posts":    len(posts),
		"total_likes":    totalLikes,
		"total_comments": totalComments,
		"total_shares":   totalShares,
		"avg_engagement": avgEngagement,
		"synced_at":      lastSynced,
	})
}

// --- Batch queue handlers ---

const (
	whatsappSoftDailyCap = 80
	whatsappHardDailyCap = 200
)

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

	// For WhatsApp batches, auto-classify recipients into tiers (Active/Quiet/New)
	// based on cached message history. The tier is stashed per-job under _note_tier
	// so the progress page can show it. The batch's min/max delay defaults to the
	// worst tier present, unless the caller explicitly set them.
	if body.Platform == "whatsapp" {
		worst := broadcast.TierActive // assume safest start
		for _, item := range body.Items {
			phone := item["phone"]
			if phone == "" {
				continue
			}
			stats, err := broadcast.FetchStats(r.Context(), s.store, phone)
			if err != nil {
				// Storage error is non-fatal — tag this recipient as "new" (safest) but only
				// update worst if it's actually worse than what we've seen.
				item["_note_tier"] = string(broadcast.TierNew)
				if tierRank(broadcast.TierNew) > tierRank(worst) {
					worst = broadcast.TierNew
				}
				continue
			}
			tier := broadcast.Classify(stats)
			item["_note_tier"] = string(tier)
			if tierRank(tier) > tierRank(worst) {
				worst = tier
			}
		}
		if body.MinDelay == 0 && body.MaxDelay == 0 {
			body.MinDelay, body.MaxDelay = broadcast.DelayFor(worst)
		}
	}

	// Daily cap enforcement (WhatsApp only). In-memory counter, resets implicitly
	// at midnight by date-string change. Soft cap warns; hard cap rejects.
	var warning string
	if body.Platform == "whatsapp" {
		today := time.Now().Format("2006-01-02")
		s.dailySendCountMu.Lock()
		projected := s.dailySendCount[today] + len(body.Items)
		if projected > whatsappHardDailyCap {
			s.dailySendCountMu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			writeJSON(w, map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("daily cap exceeded (%d/%d) — wait until tomorrow or reduce batch size", projected, whatsappHardDailyCap),
			})
			return
		}
		// Counter is incremented before Submit so concurrent or retried requests
		// cannot race past the cap; over-counting on Submit failures is intentional.
		s.dailySendCount[today] = projected
		s.dailySendCountMu.Unlock()
		if projected > whatsappSoftDailyCap {
			warning = fmt.Sprintf("daily soft cap exceeded (%d/%d) — additional sends raise Meta detection risk", projected, whatsappHardDailyCap)
		}
	}

	batchID := s.batchQueue.Submit(body.Platform, batch.JobType(body.Type), body.Items, body.MinDelay, body.MaxDelay)
	resp := map[string]interface{}{
		"ok":       true,
		"batch_id": batchID,
		"total":    len(body.Items),
		"message":  fmt.Sprintf("Batch submitted: %d %s jobs queued with %d-%ds delay between each", len(body.Items), body.Type, body.MinDelay, body.MaxDelay),
	}
	if warning != "" {
		resp["warning"] = warning
	}
	writeJSON(w, resp)
}

// tierRank ranks tiers from safest (0) to highest-risk (2) so the WhatsApp
// auto-tier logic can pick the worst tier present in a batch.
func tierRank(t broadcast.Tier) int {
	switch t {
	case broadcast.TierActive:
		return 0
	case broadcast.TierQuiet:
		return 1
	case broadcast.TierNew:
		return 2
	}
	return 1 // unknown → treat as Quiet (mid-risk, conservative)
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
		Image   string `json:"image,omitempty"`
		FromJID string `json:"from_jid,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Phone == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "phone required"})
		return
	}
	if body.Message == "" && body.Image == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "message or image required"})
		return
	}
	if body.Image != "" {
		path := body.Image
		// Resolve cowork bare-name lookup if cowork is wired and the path
		// isn't already absolute. Lets MCP callers pass "draft.png" the same
		// way the dispatcher does.
		if !filepath.IsAbs(path) && s.cowork != nil && s.cowork.Enabled() {
			entry, err := s.cowork.Find(body.Image)
			if err != nil {
				writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
				return
			}
			if entry == nil {
				writeJSON(w, map[string]interface{}{"ok": false, "error": "no cowork file matches " + body.Image})
				return
			}
			path = entry.Path
		}
		if err := s.wa.SendImage(body.Phone, path, body.Message, body.FromJID); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
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
