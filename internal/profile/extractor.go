// Package profile holds the client-enrichment extractor — passive scrape of
// conversational hints (name, family, interests, role) into a structured
// per-contact profile. The profile rows live in store.client_profiles; this
// package adds the Claude-based extraction layer on top.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"claude-bridge/internal/store"
)

// ClaudeRunner is the subset of *claude.Client used by the extractor.
type ClaudeRunner interface {
	Reply(ctx context.Context, systemPrompt, conversation string) (string, error)
}

// Extractor runs Claude on a contact's recent inbound messages to derive a
// ClientProfile. Owner-edited fields (CustomNotes) are preserved on merge.
type Extractor struct {
	Claude ClaudeRunner
}

// NewExtractor constructs an Extractor wrapping the given Claude runner.
func NewExtractor(c ClaudeRunner) *Extractor { return &Extractor{Claude: c} }

const extractorSystemPrompt = `You extract structured profile data from a person's recent messages.

Reply with ONE JSON object only — no preamble, no markdown fences. Schema:

{
  "display_name": "...",       // Preferred name. Empty if unsure.
  "aliases": ["..."],          // Nicknames or alternative names mentioned. Empty array if none.
  "language": "...",           // Primary language: "en", "zh", "ms", "ta", etc. Empty if mixed/unsure.
  "role": "...",               // One of: "lead", "client", "family", "friend", "colleague", "vendor", or empty.
  "family_notes": "...",       // Family-related facts mentioned (spouse, kids, parents). Empty if none.
  "interests": ["..."],        // Specific topics they care about (insurance products, hobbies, business sectors). Empty if none.
  "last_topics": ["..."]       // Subjects of the most recent 1-3 conversations, most recent first. Empty if not clear.
}

Rules:
- Only extract facts EXPLICITLY mentioned in the messages. Never guess.
- Keep each text field under 200 characters.
- Output empty string / empty array rather than null. Omit no keys.
- The user will supply existing profile data — preserve correct facts, only update what new messages clarify.`

// extractedPayload is the JSON Claude returns. Custom fields are never asked
// for — they come from owner edits, never extraction.
type extractedPayload struct {
	DisplayName string   `json:"display_name"`
	Aliases     []string `json:"aliases"`
	Language    string   `json:"language"`
	Role        string   `json:"role"`
	FamilyNotes string   `json:"family_notes"`
	Interests   []string `json:"interests"`
	LastTopics  []string `json:"last_topics"`
}

// Extract runs Claude on the contact's messages and returns a profile that
// merges the extraction with the existing record. The returned profile is
// ready to UpsertClientProfile; ExtractedAt is set to now.
//
// existing may be nil (first-time extraction). recent is the most-recent
// inbound messages, oldest first. jid is the contact's WhatsApp JID.
func (e *Extractor) Extract(ctx context.Context, jid string, existing *store.ClientProfile, recent []store.CachedMessage) (*store.ClientProfile, error) {
	if e.Claude == nil {
		return nil, errors.New("extractor: no Claude runner")
	}
	if len(recent) == 0 {
		return nil, errors.New("extractor: no messages to analyze")
	}

	user := buildExtractorPrompt(existing, recent)
	raw, err := e.Claude.Reply(ctx, extractorSystemPrompt, user)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}

	p, err := parseExtraction(raw)
	if err != nil {
		return nil, fmt.Errorf("parse: %w (raw=%s)", err, truncate(raw, 200))
	}

	return merge(jid, existing, p), nil
}

// buildExtractorPrompt assembles the user-side prompt: existing profile (if
// any) + recent messages.
func buildExtractorPrompt(existing *store.ClientProfile, recent []store.CachedMessage) string {
	var b strings.Builder
	if existing != nil {
		b.WriteString("Existing profile (preserve correct facts, update only what new messages clarify):\n")
		j, _ := json.MarshalIndent(map[string]any{
			"display_name": existing.DisplayName,
			"aliases":      existing.Aliases,
			"language":     existing.Language,
			"role":         existing.Role,
			"family_notes": existing.FamilyNotes,
			"interests":    existing.Interests,
			"last_topics":  existing.LastTopics,
		}, "", "  ")
		b.Write(j)
		b.WriteString("\n\n")
	}
	b.WriteString("Recent messages from this contact (oldest first):\n")
	for _, m := range recent {
		b.WriteString("- ")
		b.WriteString(strings.ReplaceAll(m.Content, "\n", " "))
		b.WriteString("\n")
	}
	b.WriteString("\nReturn the JSON profile.")
	return b.String()
}

// parseExtraction strips fences and prefix junk, then unmarshals.
func parseExtraction(raw string) (*extractedPayload, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		start := strings.Index(s, "{")
		end := strings.LastIndex(s, "}")
		if start >= 0 && end > start {
			s = s[start : end+1]
		}
	}
	var p extractedPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// merge combines existing profile + freshly extracted fields. The owner's
// custom_notes are preserved verbatim — extraction never touches them.
// aliases unioned; interests unioned; last_topics replaced (new takes
// precedence by definition).
func merge(jid string, existing *store.ClientProfile, p *extractedPayload) *store.ClientProfile {
	now := time.Now()
	out := &store.ClientProfile{
		JID:         jid,
		DisplayName: p.DisplayName,
		Aliases:     dedupe(p.Aliases),
		Language:    p.Language,
		Role:        p.Role,
		FamilyNotes: p.FamilyNotes,
		Interests:   dedupe(p.Interests),
		LastTopics:  dedupe(p.LastTopics),
		ExtractedAt: &now,
	}
	if existing == nil {
		return out
	}
	// Preserve owner-owned field unconditionally.
	out.CustomNotes = existing.CustomNotes

	// Fall back to existing when extraction returned empty (don't blank out a
	// known fact just because the latest messages didn't mention it).
	if out.DisplayName == "" {
		out.DisplayName = existing.DisplayName
	}
	if out.Language == "" {
		out.Language = existing.Language
	}
	if out.Role == "" {
		out.Role = existing.Role
	}
	if out.FamilyNotes == "" {
		out.FamilyNotes = existing.FamilyNotes
	}
	// Aliases + interests: union with existing (no shrinking).
	out.Aliases = dedupe(append(existing.Aliases, out.Aliases...))
	out.Interests = dedupe(append(existing.Interests, out.Interests...))
	// LastTopics: keep new if non-empty, else existing.
	if len(out.LastTopics) == 0 {
		out.LastTopics = existing.LastTopics
	}
	return out
}

// dedupe removes case-insensitive duplicates while preserving order.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		key := strings.ToLower(strings.TrimSpace(s))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
