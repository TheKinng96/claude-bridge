package knowledge

import (
	"path/filepath"
	"testing"
)

func TestUnderAnyRoot(t *testing.T) {
	roots := []string{"/vault/Docs", "/vault/Cowork"}
	cases := []struct {
		path string
		want bool
	}{
		{"/vault/Cowork/2026-05-20/draft.md", true},
		{"/vault/Docs/policy.pdf", true},
		{"/other/file.md", false},
	}
	for _, c := range cases {
		if got := underAnyRoot(c.path, roots); got != c.want {
			t.Errorf("underAnyRoot(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// Watcher rebuild only touches the filesystem + fsnotify; store/pipeline are
// used solely during scans, so nil deps are fine for these structural tests.
func TestSetExtraRootsWatches(t *testing.T) {
	w := NewWatcher(nil, nil)
	extra := t.TempDir()
	if err := w.SetExtraRoots(extra); err != nil {
		t.Fatalf("SetExtraRoots: %v", err)
	}
	defer w.Stop()
	if !w.Running() {
		t.Fatal("expected watcher running with one extra root")
	}
	if w.Folder() != "" {
		t.Fatalf("primary folder should be empty, got %q", w.Folder())
	}
}

func TestExtraRootsSurviveSetFolder(t *testing.T) {
	w := NewWatcher(nil, nil)
	primary := t.TempDir()
	extra := t.TempDir()

	if err := w.SetExtraRoots(extra); err != nil {
		t.Fatalf("SetExtraRoots: %v", err)
	}
	if err := w.SetFolder(primary); err != nil {
		t.Fatalf("SetFolder: %v", err)
	}
	defer w.Stop()

	w.mu.Lock()
	roots := w.allRootsLocked()
	w.mu.Unlock()
	if len(roots) != 2 {
		t.Fatalf("expected primary + extra = 2 roots, got %d: %v", len(roots), roots)
	}
	if roots[0] != primary {
		t.Errorf("primary should be first root, got %q", roots[0])
	}
}

func TestSetFolderBadPathErrors(t *testing.T) {
	w := NewWatcher(nil, nil)
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if err := w.SetFolder(missing); err == nil {
		t.Fatal("expected error for missing primary folder")
	}
}

func TestMissingExtraRootSkipped(t *testing.T) {
	w := NewWatcher(nil, nil)
	primary := t.TempDir()
	missing := filepath.Join(t.TempDir(), "no-cowork-yet")
	// Extra root missing should not fail the rebuild — primary still watched.
	if err := w.SetExtraRoots(missing); err != nil {
		t.Fatalf("SetExtraRoots with missing dir should not error: %v", err)
	}
	if err := w.SetFolder(primary); err != nil {
		t.Fatalf("SetFolder: %v", err)
	}
	defer w.Stop()
	if !w.Running() {
		t.Fatal("expected watcher running on valid primary even with missing extra")
	}
}
