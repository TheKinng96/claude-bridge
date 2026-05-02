package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// handleBatchEvents streams batch progress as Server-Sent Events.
// Query param: batch_id (optional — empty = all batches).
//
// The endpoint sends an initial snapshot of the requested batch (if any),
// then forwards every Update produced by batch.Queue. Clients should treat
// each `data: {...}` line as JSON. The connection is held open with periodic
// keep-alives until the client disconnects.
func (s *Server) handleBatchEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	batchID := r.URL.Query().Get("batch_id")
	ch, cancel := s.batchQueue.Subscribe(batchID)
	defer cancel()

	// Initial snapshot — emit current state so the client renders before the next event.
	if batchID != "" {
		if b := s.batchQueue.GetBatch(batchID); b != nil {
			snap, err := json.Marshal(map[string]any{
				"batch_id": b.ID,
				"progress": b.Progress,
				"total":    b.Total,
				"status":   b.Status,
				"snapshot": true,
			})
			if err != nil {
				log.Printf("[sse] snapshot marshal failed for %s: %v", batchID, err)
			} else {
				fmt.Fprintf(w, "data: %s\n\n", snap)
				flusher.Flush()
			}
		}
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case u, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(u)
			if err != nil {
				log.Printf("[sse] update marshal failed: %v", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}
