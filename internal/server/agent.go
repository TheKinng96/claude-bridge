package server

import (
	"encoding/json"
	"net/http"

	"claude-bridge/internal/agent"
	"claude-bridge/internal/claude"
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
		writeJSON(w, map[string]any{
			"enabled":       cfg.Enabled,
			"system_prompt": cfg.SystemPrompt,
			"flow_steps":    cfg.FlowSteps,
			"model":         cfg.Model,
		})
	case http.MethodPost:
		var body struct {
			Enabled      bool              `json:"enabled"`
			SystemPrompt string            `json:"system_prompt"`
			FlowSteps    []agent.FlowStep  `json:"flow_steps"`
			Model        string            `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
			return
		}
		cfg := agent.Config{
			Enabled:      body.Enabled,
			SystemPrompt: body.SystemPrompt,
			FlowSteps:    body.FlowSteps,
			Model:        body.Model,
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
