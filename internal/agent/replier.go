package agent

import (
	"context"
	"fmt"
	"strings"

	"claude-bridge/internal/claude"
	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/store"
)

// Replier builds the prompt and calls Claude to generate a reply.
type Replier struct {
	client   *claude.Client
	store    *store.Store
	embedder *knowledge.Embedder // nil = no vector search, fall back to FTS5
}

// NewReplier creates a Replier using the shared claude.Client and store.
func NewReplier(client *claude.Client, s *store.Store) *Replier {
	return &Replier{client: client, store: s}
}

// SetEmbedder attaches an Ollama embedder for semantic knowledge search.
func (r *Replier) SetEmbedder(e *knowledge.Embedder) {
	r.embedder = e
}

// Reply generates an AI reply for an incoming WhatsApp message.
// contactJID is the sender's JID (also used as conversationID in cached_messages).
func (r *Replier) Reply(ctx context.Context, cfg Config, contactJID, incomingBody string) (string, error) {
	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Be friendly, concise, and professional."
	}

	var sb strings.Builder

	// 1. Flow guide
	if len(cfg.FlowSteps) > 0 {
		sb.WriteString("Conversation flow guide (follow these stages in order as the conversation progresses):\n")
		for i, step := range cfg.FlowSteps {
			fmt.Fprintf(&sb, "%d. %s: %s\n", i+1, step.Name, step.Instruction)
		}
		sb.WriteString("\n")
	}

	// 2. Knowledge context — vector search (semantic) then FTS5 fallback
	if incomingBody != "" {
		hits := r.searchKnowledge(ctx, incomingBody, 3)
		if len(hits) > 0 {
			sb.WriteString("Relevant knowledge from your documents:\n")
			for i, h := range hits {
				summary := h.Document.Summary
				if len(summary) > 500 {
					summary = summary[:500] + "…"
				}
				fmt.Fprintf(&sb, "[Doc %d: %s — %s]\n", i+1, h.Document.Filename, summary)
			}
			sb.WriteString("\n")
		}
	}

	// 3. Conversation history (last 20 messages, chronological)
	// GetCachedMessages returns DESC; reverse for chronological display.
	history, _, _ := r.store.GetCachedMessages(ctx, "whatsapp", contactJID, 20)
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	if len(history) > 0 {
		sb.WriteString("Conversation history:\n")
		for _, m := range history {
			role := "Client"
			if m.IsOutgoing {
				role = "Agent"
			}
			fmt.Fprintf(&sb, "%s: %s\n", role, m.Content)
		}
		sb.WriteString("\n")
	}

	// 4. Incoming message + instruction
	fmt.Fprintf(&sb, "New message from client: %s\n\nReply naturally following the flow. Keep it concise (1-3 sentences). Return ONLY the reply text, no labels or prefixes.", incomingBody)

	return r.client.Reply(ctx, systemPrompt, sb.String())
}

// searchKnowledge tries vector search first, falls back to FTS5.
func (r *Replier) searchKnowledge(ctx context.Context, query string, limit int) []store.DocumentSearchHit {
	if r.embedder != nil {
		if vec, err := r.embedder.Embed(ctx, query); err == nil {
			if hits, err := r.store.VectorSearch(ctx, vec, limit); err == nil && len(hits) > 0 {
				return hits
			}
		}
	}
	hits, _ := r.store.SearchDocuments(ctx, query, "", "", limit)
	return hits
}
