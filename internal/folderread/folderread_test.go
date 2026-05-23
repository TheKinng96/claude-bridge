// internal/folderread/folderread_test.go
package folderread

import (
	"os"
	"path/filepath"
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
