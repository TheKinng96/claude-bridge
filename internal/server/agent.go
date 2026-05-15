package server

import (
	"encoding/json"
	"net/http"

	"claude-bridge/internal/agent"
	"claude-bridge/internal/claude"
	"claude-bridge/internal/connectors/telegram"
)

func (s *Server) handleAgent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(agentHTML))
}

func (s *Server) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		cfg, _ := agent.LoadConfig(ctx, s.store)
		ownerJIDs := cfg.OwnerJIDs
		if ownerJIDs == nil {
			ownerJIDs = []string{}
		}
		ownerTGIDs := cfg.OwnerTelegramIDs
		if ownerTGIDs == nil {
			ownerTGIDs = []int64{}
		}
		writeJSON(w, map[string]any{
			"enabled":             cfg.Enabled,
			"system_prompt":       cfg.SystemPrompt,
			"flow_steps":          cfg.FlowSteps,
			"model":               cfg.Model,
			"global_reply_mode":   cfg.GlobalReplyMode,
			"owner_jids":          ownerJIDs,
			"auto_sync_frequency": cfg.AutoSyncFrequency,
			"telegram_bot_token":  cfg.TelegramBotToken,
			"owner_telegram_ids":  ownerTGIDs,
		})
	case http.MethodPost:
		var body struct {
			Enabled           bool             `json:"enabled"`
			SystemPrompt      string           `json:"system_prompt"`
			FlowSteps         []agent.FlowStep `json:"flow_steps"`
			Model             string           `json:"model"`
			GlobalReplyMode   string           `json:"global_reply_mode"`
			OwnerJIDs         []string         `json:"owner_jids"`
			AutoSyncFrequency string           `json:"auto_sync_frequency"`
			TelegramBotToken  string           `json:"telegram_bot_token"`
			OwnerTelegramIDs  []int64          `json:"owner_telegram_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
			return
		}
		cfg := agent.Config{
			Enabled:           body.Enabled,
			SystemPrompt:      body.SystemPrompt,
			FlowSteps:         body.FlowSteps,
			Model:             body.Model,
			GlobalReplyMode:   body.GlobalReplyMode,
			OwnerJIDs:         body.OwnerJIDs,
			AutoSyncFrequency: body.AutoSyncFrequency,
			TelegramBotToken:  body.TelegramBotToken,
			OwnerTelegramIDs:  body.OwnerTelegramIDs,
		}
		if cfg.GlobalReplyMode == "" {
			cfg.GlobalReplyMode = "auto"
		}
		if cfg.Model == "" {
			cfg.Model = claude.DefaultModel
		}
		if len(cfg.FlowSteps) == 0 {
			cfg.FlowSteps = agent.DefaultFlowSteps
		}
		if err := agent.SaveConfig(ctx, s.store, cfg); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleTelegramTest verifies a Telegram bot token by calling getMe.
// POST body: {"token": "..."} — uses the posted token rather than saved one so
// the user can verify before persisting.
func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Token == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "token required"})
		return
	}
	c := telegram.New(body.Token, nil)
	me, err := c.GetMe(r.Context())
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{
		"ok":         true,
		"id":         me.ID,
		"first_name": me.FirstName,
		"username":   me.Username,
	})
}

func (s *Server) handleAgentReplies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	msgs, _, err := s.store.GetCachedMessages(ctx, "whatsapp", "", 50)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Filter to agent-sent replies only
	var replies []any
	for _, m := range msgs {
		if m.IsOutgoing && m.SenderName == "Agent" {
			replies = append(replies, m)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "replies": replies})
}
