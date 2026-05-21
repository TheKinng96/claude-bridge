package profile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"claude-bridge/internal/store"
)

type fakeClaude struct {
	reply  string
	err    error
	lastS  string
	lastU  string
}

func (f *fakeClaude) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastS = system
	f.lastU = user
	return f.reply, f.err
}

func msgs(bodies ...string) []store.CachedMessage {
	out := make([]store.CachedMessage, len(bodies))
	for i, b := range bodies {
		out[i] = store.CachedMessage{Content: b}
	}
	return out
}

func TestExtract_ParsesAllFields(t *testing.T) {
	c := &fakeClaude{reply: `{
		"display_name": "Alice",
		"aliases": ["Ally", "A."],
		"language": "en",
		"role": "client",
		"family_notes": "Has 2 kids, daughter Mia 8, son Leo 5.",
		"interests": ["family insurance", "savings plans"],
		"last_topics": ["renewal", "kids' policy"]
	}`}
	e := NewExtractor(c)
	p, err := e.Extract(context.Background(), "601@s", nil, msgs("hi"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.DisplayName != "Alice" {
		t.Errorf("display_name=%q", p.DisplayName)
	}
	if len(p.Aliases) != 2 || p.Aliases[0] != "Ally" {
		t.Errorf("aliases=%v", p.Aliases)
	}
	if p.Role != "client" {
		t.Errorf("role=%s", p.Role)
	}
	if !strings.Contains(p.FamilyNotes, "Mia") {
		t.Errorf("family_notes=%q", p.FamilyNotes)
	}
	if len(p.Interests) != 2 {
		t.Errorf("interests=%v", p.Interests)
	}
	if p.JID != "601@s" {
		t.Errorf("jid not propagated: %s", p.JID)
	}
	if p.ExtractedAt == nil {
		t.Errorf("ExtractedAt should be set")
	}
}

func TestExtract_PreservesCustomNotes(t *testing.T) {
	c := &fakeClaude{reply: `{"display_name":"Alice","aliases":[],"language":"en","role":"client","family_notes":"","interests":[],"last_topics":[]}`}
	e := NewExtractor(c)
	existing := &store.ClientProfile{
		JID:         "601@s",
		CustomNotes: "Owner says: VIP, prefers WhatsApp not email.",
	}
	p, err := e.Extract(context.Background(), "601@s", existing, msgs("hi"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.CustomNotes != "Owner says: VIP, prefers WhatsApp not email." {
		t.Errorf("custom_notes lost: %q", p.CustomNotes)
	}
}

func TestExtract_EmptyFieldsFallBackToExisting(t *testing.T) {
	c := &fakeClaude{reply: `{"display_name":"","aliases":[],"language":"","role":"","family_notes":"","interests":[],"last_topics":[]}`}
	e := NewExtractor(c)
	existing := &store.ClientProfile{
		JID:         "601@s",
		DisplayName: "Alice",
		Language:    "en",
		Role:        "client",
		FamilyNotes: "kids: Mia, Leo",
	}
	p, _ := e.Extract(context.Background(), "601@s", existing, msgs("hi"))
	if p.DisplayName != "Alice" {
		t.Errorf("expected fallback, got display_name=%q", p.DisplayName)
	}
	if p.Role != "client" {
		t.Errorf("expected fallback, got role=%s", p.Role)
	}
	if p.FamilyNotes != "kids: Mia, Leo" {
		t.Errorf("expected fallback, got family_notes=%q", p.FamilyNotes)
	}
}

func TestExtract_AliasesUnion(t *testing.T) {
	c := &fakeClaude{reply: `{"display_name":"Alice","aliases":["Ally","Liz"],"language":"en","role":"","family_notes":"","interests":[],"last_topics":[]}`}
	e := NewExtractor(c)
	existing := &store.ClientProfile{Aliases: []string{"Ally", "A."}}
	p, _ := e.Extract(context.Background(), "601@s", existing, msgs("hi"))
	// existing Ally + A. unioned with new Ally + Liz = Ally, A., Liz (case-insensitive dedupe)
	if len(p.Aliases) != 3 {
		t.Errorf("expected union of 3, got %v", p.Aliases)
	}
	got := strings.ToLower(strings.Join(p.Aliases, ","))
	if !strings.Contains(got, "liz") || !strings.Contains(got, "a.") {
		t.Errorf("missing aliases: %v", p.Aliases)
	}
}

func TestExtract_HandlesCodeFences(t *testing.T) {
	c := &fakeClaude{reply: "```json\n{\"display_name\":\"Bob\",\"aliases\":[],\"language\":\"en\",\"role\":\"\",\"family_notes\":\"\",\"interests\":[],\"last_topics\":[]}\n```"}
	e := NewExtractor(c)
	p, err := e.Extract(context.Background(), "602@s", nil, msgs("hi"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.DisplayName != "Bob" {
		t.Errorf("got %q", p.DisplayName)
	}
}

func TestExtract_PrependsExistingProfileToPrompt(t *testing.T) {
	c := &fakeClaude{reply: `{"display_name":"Alice","aliases":[],"language":"en","role":"client","family_notes":"","interests":[],"last_topics":[]}`}
	e := NewExtractor(c)
	existing := &store.ClientProfile{DisplayName: "Alice", Role: "lead"}
	if _, err := e.Extract(context.Background(), "601@s", existing, msgs("hello")); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(c.lastU, "Existing profile") {
		t.Errorf("prompt missing existing-profile preamble: %s", c.lastU)
	}
	if !strings.Contains(c.lastU, "\"role\": \"lead\"") {
		t.Errorf("prompt missing existing role: %s", c.lastU)
	}
}

func TestExtract_ClaudeErrorPropagates(t *testing.T) {
	c := &fakeClaude{err: errors.New("rate limit")}
	e := NewExtractor(c)
	_, err := e.Extract(context.Background(), "601@s", nil, msgs("hi"))
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("expected rate limit err, got %v", err)
	}
}

func TestExtract_NoMessagesErrors(t *testing.T) {
	e := NewExtractor(&fakeClaude{})
	_, err := e.Extract(context.Background(), "601@s", nil, nil)
	if err == nil {
		t.Errorf("expected error for empty messages")
	}
}

func TestExtract_NilClaudeErrors(t *testing.T) {
	e := NewExtractor(nil)
	_, err := e.Extract(context.Background(), "601@s", nil, msgs("hi"))
	if err == nil {
		t.Errorf("expected error for nil Claude")
	}
}

func TestExtract_MalformedJSONErrors(t *testing.T) {
	c := &fakeClaude{reply: `not json at all`}
	e := NewExtractor(c)
	_, err := e.Extract(context.Background(), "601@s", nil, msgs("hi"))
	if err == nil {
		t.Errorf("expected parse error")
	}
}

func TestDedupe_CaseInsensitive(t *testing.T) {
	out := dedupe([]string{"Alice", "alice", "Bob", "ALICE"})
	if len(out) != 2 {
		t.Errorf("got %v, want 2", out)
	}
}

func TestDedupe_EmptyAndWhitespace(t *testing.T) {
	out := dedupe([]string{"", "  ", "Bob", "bob "})
	if len(out) != 1 || out[0] != "Bob" {
		t.Errorf("got %v", out)
	}
}
