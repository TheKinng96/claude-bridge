package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"claude-bridge/internal/store"
)

// GET /contacts — page (HTML served by html_contacts.go in Task 12)
func (s *Server) handleContactsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(contactsPage()))
}

// GET /api/contacts
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	contacts, groupsByContact, err := s.store.ListContactsWithGroups(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	type groupItem struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		ReplyMode string `json:"reply_mode"`
	}
	type contactItem struct {
		ID          int64       `json:"id"`
		JID         string      `json:"jid"`
		PushName    string      `json:"push_name"`
		Platform    string      `json:"platform"`
		FirstSeenAt string      `json:"first_seen_at"`
		Groups      []groupItem `json:"groups"`
	}
	out := make([]contactItem, 0, len(contacts))
	for _, c := range contacts {
		groups := groupsByContact[c.ID]
		gItems := make([]groupItem, 0, len(groups))
		for _, g := range groups {
			gItems = append(gItems, groupItem{ID: g.ID, Name: g.Name, Type: g.Type, ReplyMode: g.ReplyMode})
		}
		out = append(out, contactItem{
			ID:          c.ID,
			JID:         c.JID,
			PushName:    c.PushName,
			Platform:    c.Platform,
			FirstSeenAt: c.FirstSeenAt.Format("2006-01-02 15:04"),
			Groups:      gItems,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "contacts": out})
}

// GET /api/groups
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	groups, err := s.store.ListGroups(ctx)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	type groupItem struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Type         string `json:"type"`
		ReplyMode    string `json:"reply_mode"`
		ContactCount int    `json:"contact_count"`
	}
	out := make([]groupItem, 0, len(groups))
	for _, g := range groups {
		contacts, _ := s.store.ListGroupContacts(ctx, g.ID)
		out = append(out, groupItem{ID: g.ID, Name: g.Name, Type: g.Type, ReplyMode: g.ReplyMode, ContactCount: len(contacts)})
	}
	writeJSON(w, map[string]any{"ok": true, "groups": out})
}

// POST /api/groups
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		ReplyMode string `json:"reply_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if body.Name == "" || (body.Type != "manual" && body.Type != "auto") ||
		(body.ReplyMode != "auto" && body.ReplyMode != "review" && body.ReplyMode != "off") {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid fields"})
		return
	}
	g, err := s.store.CreateGroup(r.Context(), body.Name, body.Type, body.ReplyMode)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "group": g})
}

// PUT /api/groups/{id}
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	var body struct {
		Name      string `json:"name"`
		ReplyMode string `json:"reply_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	if err := s.store.UpdateGroup(r.Context(), id, body.Name, body.ReplyMode); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// DELETE /api/groups/{id}
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// POST /api/contacts/{jid}/groups  body: {"group_id": 1}
func (s *Server) handleAssignContactGroup(w http.ResponseWriter, r *http.Request) {
	jid := extractContactJID(r.URL.Path)
	if jid == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "missing jid"})
		return
	}
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
		return
	}
	ctx := r.Context()
	contact, err := s.store.GetContact(ctx, jid)
	if err != nil || contact == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "contact not found"})
		return
	}
	if err := s.store.AssignContactToGroup(ctx, contact.ID, body.GroupID, "manual"); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// DELETE /api/contacts/{jid}/groups/{group_id}
func (s *Server) handleRemoveContactGroup(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/contacts/")
	parts := strings.SplitN(path, "/groups/", 2)
	if len(parts) != 2 {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid path"})
		return
	}
	jid := parts[0]
	groupID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid group_id"})
		return
	}
	ctx := r.Context()
	contact, err := s.store.GetContact(ctx, jid)
	if err != nil || contact == nil {
		writeJSON(w, map[string]any{"ok": false, "error": "contact not found"})
		return
	}
	if err := s.store.RemoveContactFromGroup(ctx, contact.ID, groupID); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// GET /api/groups/{id}/contacts
func (s *Server) handleListGroupContacts(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
		return
	}
	contacts, err := s.store.ListGroupContacts(r.Context(), id)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if contacts == nil {
		contacts = []store.Contact{}
	}
	writeJSON(w, map[string]any{"ok": true, "contacts": contacts})
}

// Dispatch: /api/contacts/{jid}/groups and /api/contacts/{jid}/groups/{id}
func (s *Server) handleContactsSubroute(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.Contains(path, "/groups/") && r.Method == http.MethodDelete {
		s.handleRemoveContactGroup(w, r)
	} else if strings.HasSuffix(path, "/groups") && r.Method == http.MethodPost {
		s.handleAssignContactGroup(w, r)
	} else {
		http.NotFound(w, r)
	}
}

// Dispatch: GET+POST /api/groups
func (s *Server) handleGroupsRoute(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListGroups(w, r)
	case http.MethodPost:
		s.handleCreateGroup(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Dispatch: PUT/DELETE /api/groups/{id} and GET /api/groups/{id}/contacts
func (s *Server) handleGroupsSubroute(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if strings.HasSuffix(path, "/contacts") {
		s.handleListGroupContacts(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.handleUpdateGroup(w, r)
	case http.MethodDelete:
		s.handleDeleteGroup(w, r)
	default:
		http.NotFound(w, r)
	}
}

func parseIDFromPath(path, prefix string) (int64, error) {
	s := strings.TrimPrefix(path, prefix)
	s = strings.SplitN(s, "/", 2)[0]
	return strconv.ParseInt(s, 10, 64)
}

func extractContactJID(path string) string {
	s := strings.TrimPrefix(path, "/api/contacts/")
	parts := strings.SplitN(s, "/", 2)
	return parts[0]
}
