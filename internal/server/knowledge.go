package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"claude-bridge/internal/claude"
	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/store"
)

// handleKnowledge serves the dashboard page for document ingestion.
func (s *Server) handleKnowledge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, knowledgeHTML)
}

// handleKnowledgeConfig is GET/POST for folder path + API key + model.
func (s *Server) handleKnowledgeConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.Method {
	case http.MethodGet:
		cfg, _ := knowledge.LoadConfig(ctx, s.store)
		writeJSON(w, map[string]interface{}{
			"folder_path":        cfg.FolderPath,
			"model":              cfg.Model,
			"last_scan_at":       cfg.LastScanAt,
			"classify_delay_sec": cfg.ClassifyDelaySec,
			"session_limit":      cfg.SessionLimit,
			"watcher_running":    s.knowWatcher != nil && s.knowWatcher.Running(),
			"watcher_folder":     watcherFolder(s.knowWatcher),
			"api_calls":          apiCallsOf(s.knowClient),
		})
	case http.MethodPost:
		var body struct {
			FolderPath       string `json:"folder_path"`
			Model            string `json:"model"`
			ClassifyDelaySec int    `json:"classify_delay_sec"`
			SessionLimit     int    `json:"session_limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
			return
		}
		cfg := knowledge.Config{
			FolderPath:       body.FolderPath,
			Model:            body.Model,
			ClassifyDelaySec: body.ClassifyDelaySec,
			SessionLimit:     body.SessionLimit,
		}
		if cfg.Model == "" {
			cfg.Model = claude.DefaultModel
		}
		if err := knowledge.SaveConfig(ctx, s.store, cfg); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		if s.knowClient != nil {
			s.knowClient.SetModel(cfg.Model)
		}
		// Apply folder to watcher — swap in place.
		if s.knowWatcher != nil {
			if err := s.knowWatcher.SetFolder(cfg.FolderPath); err != nil {
				writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
				return
			}
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDocumentsList returns documents with optional filters.
func (s *Server) handleDocumentsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	filter := store.DocumentFilter{
		DocType:  q.Get("doc_type"),
		Product:  q.Get("product"),
		Language: q.Get("language"),
		Status:   q.Get("status"),
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		filter.Limit = n
	}
	docs, err := s.store.ListDocuments(ctx, filter)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	stats, _ := s.store.DocumentStats(ctx)
	writeJSON(w, map[string]interface{}{
		"ok":        true,
		"documents": docs,
		"stats":     stats,
		"api_calls": apiCallsOf(s.knowClient),
	})
}

// handleDocumentByID supports GET /api/documents/{id},
// DELETE /api/documents/{id}, and POST /api/documents/{id}/reingest.
func (s *Server) handleDocumentByID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rest := strings.TrimPrefix(r.URL.Path, "/api/documents/")
	if rest == "" || rest == "search" || rest == "rescan" {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(rest, "/")
	idStr := parts[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) > 1 && parts[1] == "reingest" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		doc, err := s.store.GetDocument(ctx, id)
		if err != nil || doc == nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "not found"})
			return
		}
		if s.knowPipeline != nil {
			go func(p string) {
				_ = s.knowPipeline.IngestOnce(context.Background(), p)
			}(doc.Path)
		}
		writeJSON(w, map[string]interface{}{"ok": true})
		return
	}
	switch r.Method {
	case http.MethodGet:
		doc, err := s.store.GetDocument(ctx, id)
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		if doc == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "document": doc})
	case http.MethodDelete:
		if err := s.store.DeleteDocument(ctx, id); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleDocumentsSearch runs FTS5 query.
func (s *Server) handleDocumentsSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "POST or GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()
	var query, language, docType string
	limit := 20
	if r.Method == http.MethodPost {
		var body struct {
			Query    string `json:"query"`
			Language string `json:"language"`
			DocType  string `json:"doc_type"`
			Limit    int    `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "invalid JSON"})
			return
		}
		query, language, docType = body.Query, body.Language, body.DocType
		if body.Limit > 0 {
			limit = body.Limit
		}
	} else {
		q := r.URL.Query()
		query = q.Get("q")
		language = q.Get("language")
		docType = q.Get("doc_type")
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			limit = n
		}
	}
	if query == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "query required"})
		return
	}
	hits, err := s.store.SearchDocuments(ctx, query, language, docType, limit)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "hits": hits, "query": query})
}

// handleDocumentsRescan triggers a full walk of the configured folder.
func (s *Server) handleDocumentsRescan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.knowWatcher == nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "knowledge subsystem not initialized"})
		return
	}
	s.knowWatcher.Rescan()
	_ = knowledge.UpdateLastScan(r.Context(), s.store)
	writeJSON(w, map[string]interface{}{"ok": true})
}

// handleBrowseFolder opens a native macOS folder picker via osascript and returns the chosen path.
func (s *Server) handleBrowseFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	out, err := exec.CommandContext(r.Context(),
		"osascript", "-e", "choose folder", "-e", "POSIX path of result",
	).Output()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "no folder selected"})
		return
	}
	path := strings.TrimRight(string(out), "\n/")
	writeJSON(w, map[string]interface{}{"ok": true, "path": path})
}

func watcherFolder(w *knowledge.Watcher) string {
	if w == nil {
		return ""
	}
	return w.Folder()
}

func apiCallsOf(c *claude.Client) int64 {
	if c == nil {
		return 0
	}
	return c.APICalls()
}
