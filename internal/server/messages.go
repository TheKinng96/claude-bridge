package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claude-bridge/internal/store"
)

// GET /messages — page
func (s *Server) handleMessagesPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(messagesPage()))
}

// GET /api/pending-replies?status=pending
func (s *Server) handleListPendingReplies(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	replies, err := s.store.ListPendingReplies(r.Context(), status)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if replies == nil {
		replies = []store.PendingReply{}
	}
	writeJSON(w, map[string]any{"ok": true, "replies": replies})
}

// POST /api/pending-replies/{id}/approve
func (s *Server) handleApprovePendingReply(w http.ResponseWriter, r *http.Request) {
	id, err := parsePendingReplyID(r.URL.Path, "approve")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	ctx := r.Context()
	p, err := s.store.GetPendingReply(ctx, id)
	if err != nil || p == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if p.Status != "pending" {
		writeJSON(w, map[string]any{"ok": false, "error": "already reviewed"})
		return
	}
	phone := strings.Split(p.ContactJID, "@")[0]
	if err := s.wa.SendMessage(phone, p.ProposedReply, p.AccountJID); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.UpdatePendingReplyStatus(ctx, id, "sent")
	now := time.Now()
	_ = s.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       p.Platform,
		ConversationID: p.ContactJID,
		MessageID:      "agent-approved-" + strconv.FormatInt(id, 10),
		SenderID:       p.AccountJID,
		SenderName:     "Agent",
		Content:        p.ProposedReply,
		Timestamp:      now,
		IsOutgoing:     true,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// POST /api/pending-replies/{id}/reject
func (s *Server) handleRejectPendingReply(w http.ResponseWriter, r *http.Request) {
	id, err := parsePendingReplyID(r.URL.Path, "reject")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	if err := s.store.UpdatePendingReplyStatus(r.Context(), id, "rejected"); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// POST /api/pending-replies/{id}/edit-send  body: {"reply": "..."}
func (s *Server) handleEditSendPendingReply(w http.ResponseWriter, r *http.Request) {
	id, err := parsePendingReplyID(r.URL.Path, "edit-send")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	var body struct {
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reply == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid body"})
		return
	}
	ctx := r.Context()
	p, err := s.store.GetPendingReply(ctx, id)
	if err != nil || p == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "not found"})
		return
	}
	if p.Status != "pending" {
		writeJSON(w, map[string]any{"ok": false, "error": "already reviewed"})
		return
	}
	_ = s.store.UpdatePendingReplyContent(ctx, id, body.Reply)
	phone := strings.Split(p.ContactJID, "@")[0]
	if err := s.wa.SendMessage(phone, body.Reply, p.AccountJID); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = s.store.UpdatePendingReplyStatus(ctx, id, "sent")
	now := time.Now()
	_ = s.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       p.Platform,
		ConversationID: p.ContactJID,
		MessageID:      "agent-edited-" + strconv.FormatInt(id, 10),
		SenderID:       p.AccountJID,
		SenderName:     "Agent",
		Content:        body.Reply,
		Timestamp:      now,
		IsOutgoing:     true,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// Dispatch: /api/pending-replies/{id}/{action}
func (s *Server) handlePendingRepliesSubroute(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/approve"):
		s.handleApprovePendingReply(w, r)
	case strings.HasSuffix(path, "/reject"):
		s.handleRejectPendingReply(w, r)
	case strings.HasSuffix(path, "/edit-send"):
		s.handleEditSendPendingReply(w, r)
	default:
		http.NotFound(w, r)
	}
}

func parsePendingReplyID(path, action string) (int64, error) {
	s := strings.TrimPrefix(path, "/api/pending-replies/")
	s = strings.TrimSuffix(s, "/"+action)
	return strconv.ParseInt(s, 10, 64)
}
