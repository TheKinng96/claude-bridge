// internal/folderread/folderread_test.go
package folderread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "hello")
	writeFile(t, dir, "sub/b.txt", "world")
	writeFile(t, dir, ".hidden", "x")

	r := New(dir)
	if !r.Enabled() {
		t.Fatal("expected enabled")
	}
	got, err := r.List("")
	if err != nil {
		t.Fatal(err)
	}
	// Expect a.md (file) and sub (dir); .hidden skipped.
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(got), got)
	}
	var sawDir, sawFile bool
	for _, e := range got {
		if e.Name == "sub" && e.IsDir {
			sawDir = true
		}
		if e.Name == "a.md" && !e.IsDir && e.IsText {
			sawFile = true
		}
	}
	if !sawDir || !sawFile {
		t.Fatalf("missing expected entries: %+v", got)
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	r := New(t.TempDir())
	got, err := r.List("nope")
	if err != nil {
		t.Fatalf("missing subdir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestDisabled(t *testing.T) {
	r := New("")
	if r.Enabled() {
		t.Fatal("empty path should be disabled")
	}
	if _, err := r.List(""); err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}

func TestReadText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/note.md", "line1\nline2")
	r := New(dir)
	text, e, err := r.Read("sub/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if text != "line1\nline2" {
		t.Fatalf("unexpected content %q", text)
	}
	if e == nil || !e.IsText || e.Name != "note.md" {
		t.Fatalf("unexpected entry %+v", e)
	}
}

func TestReadBinaryPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pic.png", "\x89PNGblob")
	r := New(dir)
	text, e, err := r.Read("pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if e.IsText || !strings.Contains(text, "binary file") {
		t.Fatalf("want binary placeholder, got %q (%+v)", text, e)
	}
}

func TestReadTruncates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.txt", strings.Repeat("a", MaxReadBytes+500))
	r := New(dir)
	text, _, err := r.Read("big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(text, "…(truncated)") {
		t.Fatal("expected truncation marker")
	}
}

func TestReadTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ok.txt", "x")
	r := New(dir)
	if _, _, err := r.Read("../../../etc/passwd"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}
