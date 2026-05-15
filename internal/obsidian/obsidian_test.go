package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-bridge/internal/store"
)

func TestWriter_DisabledNoOp(t *testing.T) {
	w := New("")
	if w.Enabled() {
		t.Errorf("empty path should be disabled")
	}
	if err := w.WriteClient(&store.ClientProfile{JID: "601@s.whatsapp.net"}); err != nil {
		t.Errorf("disabled writer should return nil, got %v", err)
	}
	if err := w.WriteTopicStubs([]string{"x"}); err != nil {
		t.Errorf("disabled writer should return nil, got %v", err)
	}
}

func TestWriteClient_CreatesFileAtExpectedPath(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	err := w.WriteClient(&store.ClientProfile{
		JID:         "601@s.whatsapp.net",
		DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(dir, "Clients", "Alice.md")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}

func TestWriteClient_FallsBackToShortJIDWhenNoName(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	err := w.WriteClient(&store.ClientProfile{JID: "601@s.whatsapp.net"})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(dir, "Clients", "601.md")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}

func TestWriteClient_SanitizesUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	err := w.WriteClient(&store.ClientProfile{
		JID:         "601@s",
		DisplayName: `Alice/Bob:test*<>`,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	// "/", ":", "*", "<", ">" all replaced with "_"
	want := filepath.Join(dir, "Clients", "Alice_Bob_test___.md")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected file at %s: %v", want, err)
	}
}

func TestWriteClient_BodyHasFrontmatterAndSections(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	p := &store.ClientProfile{
		JID:         "601@s.whatsapp.net",
		DisplayName: "Alice",
		Role:        "client",
		Language:    "en",
		Aliases:     []string{"Ally", "A."},
		FamilyNotes: "kids: Mia, Leo",
		Interests:   []string{"insurance", "savings"},
		LastTopics:  []string{"renewal", "kids' policy"},
		CustomNotes: "VIP",
	}
	if err := w.WriteClient(p); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "Clients", "Alice.md"))
	body := string(raw)

	checks := []string{
		"---\njid: 601@s.whatsapp.net\n",
		"role: client",
		"language: en",
		"aliases: [Ally, A.]",
		"interests: [insurance, savings]",
		"\n# Alice\n",
		"## Family\nkids: Mia, Leo",
		"## Recent Topics",
		"- [[renewal]]",
		"- [[kids' policy]]",
		"## Interests\n- insurance",
		"## Custom Notes\nVIP",
		"Auto-generated. Edits outside",
	}
	for _, c := range checks {
		if !strings.Contains(body, c) {
			t.Errorf("missing %q in body:\n%s", c, body)
		}
	}
}

func TestWriteClient_EmptyOptionalsOmitted(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	p := &store.ClientProfile{
		JID:         "601@s",
		DisplayName: "Bob",
	}
	if err := w.WriteClient(p); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "Clients", "Bob.md"))
	body := string(raw)
	if strings.Contains(body, "## Family") {
		t.Errorf("family section should be omitted when empty: %s", body)
	}
	if strings.Contains(body, "## Recent Topics") {
		t.Errorf("topics section should be omitted when empty: %s", body)
	}
	if !strings.Contains(body, "## Custom Notes") {
		t.Errorf("custom notes section should always be present (placeholder for user edits)")
	}
}

func TestWriteTopicStubs_CreatesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	err := w.WriteTopicStubs([]string{"renewal", "kids' policy"})
	if err != nil {
		t.Fatalf("write stubs: %v", err)
	}
	for _, n := range []string{"renewal.md", "kids' policy.md"} {
		path := filepath.Join(dir, "Topics", n)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected stub %s: %v", path, err)
		}
	}
}

func TestWriteTopicStubs_IdempotentSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	// Pre-create with custom content.
	topicsDir := filepath.Join(dir, "Topics")
	_ = os.MkdirAll(topicsDir, 0o755)
	existing := filepath.Join(topicsDir, "renewal.md")
	custom := []byte("# renewal\n\nMy hand-written notes here.\n")
	if err := os.WriteFile(existing, custom, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := w.WriteTopicStubs([]string{"renewal", "new_topic"}); err != nil {
		t.Fatalf("stubs: %v", err)
	}

	got, _ := os.ReadFile(existing)
	if string(got) != string(custom) {
		t.Errorf("existing file overwritten:\nwant=%q\ngot=%q", custom, got)
	}
	// New stub must still be created
	if _, err := os.Stat(filepath.Join(topicsDir, "new_topic.md")); err != nil {
		t.Errorf("new stub not created: %v", err)
	}
}

func TestWriteTopicStubs_SkipsEmptyNames(t *testing.T) {
	dir := t.TempDir()
	w := New(dir)
	err := w.WriteTopicStubs([]string{"", "   ", "real"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	files, _ := os.ReadDir(filepath.Join(dir, "Topics"))
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

func TestYamlEscape_QuotesWhenNeeded(t *testing.T) {
	cases := map[string]string{
		"":           `""`,
		"plain":      "plain",
		"has: colon": `"has: colon"`,
		" leading":   `" leading"`,
		"has \"q":    `"has \"q"`,
	}
	for in, want := range cases {
		if got := yamlEscape(in); got != want {
			t.Errorf("yamlEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
