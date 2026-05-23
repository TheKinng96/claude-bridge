// internal/obsidian/reader_test.go
package obsidian

import (
	"os"
	"path/filepath"
	"testing"
)

func writeNote(t *testing.T, vault, rel, body string) {
	t.Helper()
	p := filepath.Join(vault, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadNote(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "Clients/Alice.md",
		"---\nrole: client\nlanguage: en\n---\n# Alice\n\nSees [[Tan Policy]] and #vip stuff.\n")
	r := NewReader(vault)
	if !r.Enabled() {
		t.Fatal("expected enabled")
	}
	n, err := r.ReadNote("Alice")
	if err != nil {
		t.Fatal(err)
	}
	if n.Frontmatter["role"] != "client" || n.Frontmatter["language"] != "en" {
		t.Fatalf("frontmatter parse failed: %+v", n.Frontmatter)
	}
	if len(n.OutLinks) != 1 || n.OutLinks[0] != "Tan Policy" {
		t.Fatalf("outlinks parse failed: %+v", n.OutLinks)
	}
	if len(n.Tags) != 1 || n.Tags[0] != "vip" {
		t.Fatalf("tags parse failed: %+v", n.Tags)
	}
	if want := "# Alice"; n.Body[:len(want)] != want {
		t.Fatalf("body should start after frontmatter, got %q", n.Body)
	}
}

func TestReadNoteResolvesWikilinkAndAlias(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "Tan Policy.md", "# Tan Policy\n")
	r := NewReader(vault)
	n, err := r.ReadNote("[[Tan Policy|the policy]]")
	if err != nil {
		t.Fatalf("alias form should resolve: %v", err)
	}
	if n.Name != "Tan Policy" {
		t.Fatalf("unexpected note name %q", n.Name)
	}
}

func TestReadNoteNotFound(t *testing.T) {
	r := NewReader(t.TempDir())
	if _, err := r.ReadNote("Ghost"); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestBacklinks(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "Tan Policy.md", "# Tan Policy\n")
	writeNote(t, vault, "Clients/Alice.md", "Refers to [[Tan Policy]].\n")
	writeNote(t, vault, "Clients/Bob.md", "Also [[Tan Policy|the plan]] here.\n")
	writeNote(t, vault, "Clients/Carol.md", "No links.\n")

	r := NewReader(vault)
	got, err := r.Backlinks("Tan Policy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 backlinks, got %v", got)
	}
	// sorted: Clients/Alice, Clients/Bob
	if got[0] != "Clients/Alice" || got[1] != "Clients/Bob" {
		t.Fatalf("unexpected backlinks %v", got)
	}
}
