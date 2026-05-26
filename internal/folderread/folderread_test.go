// internal/folderread/folderread_test.go
package folderread

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// resolve must ERROR on ".." components — not silently remap them into the
	// root. This passes because resolve returns an error, not because the
	// remapped target file happens to be absent.
	if _, _, err := r.Read("../../../etc/passwd"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestReadSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	// Create a secret file OUTSIDE the root.
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "TOP SECRET")
	// Symlink the outside dir to a name inside the root.
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	r := New(root)
	text, _, err := r.Read("link/secret.txt")
	if err == nil {
		t.Fatalf("expected symlink escape to be rejected, got content %q", text)
	}
}

// A bare filename (no slash) that isn't at the root resolves by searching the
// whole folder — the KB folder is the superset of the Vault and Cowork
// subfolders, so the owner can say "read weather.txt" without knowing its path.
func TestReadBareNameFindsInSubdir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Vault/weather.txt", "sunny 28C")
	r := New(dir)
	text, e, err := r.Read("weather.txt")
	if err != nil {
		t.Fatalf("bare-name search should find Vault/weather.txt: %v", err)
	}
	if text != "sunny 28C" {
		t.Fatalf("unexpected content %q", text)
	}
	if e == nil || e.RelPath != "Vault/weather.txt" {
		t.Fatalf("entry should report real relpath, got %+v", e)
	}
}

// On multiple matches, the newest file wins (mirrors cowork fuzzy behavior).
func TestReadBareNameNewestWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "old/report.md", "OLD")
	writeFile(t, dir, "new/report.md", "NEW")
	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "old/report.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "new/report.md"), recent, recent); err != nil {
		t.Fatal(err)
	}
	r := New(dir)
	text, _, err := r.Read("report.md")
	if err != nil {
		t.Fatal(err)
	}
	if text != "NEW" {
		t.Fatalf("newest match should win, got %q", text)
	}
}

// A bare name with no extension matches a file by stem ("weather" -> weather.txt).
func TestReadBareNameStemMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Vault/weather.txt", "sunny")
	r := New(dir)
	text, _, err := r.Read("weather")
	if err != nil {
		t.Fatalf("stem match should find weather.txt: %v", err)
	}
	if text != "sunny" {
		t.Fatalf("unexpected content %q", text)
	}
}

// A bare name that exists nowhere returns a clear error naming the file and the
// fact that the whole folder was searched — not a raw stat path error.
func TestReadBareNameMissError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Vault/other.md", "x")
	r := New(dir)
	_, _, err := r.Read("weather.txt")
	if err == nil {
		t.Fatal("expected miss error for absent bare name")
	}
	if !strings.Contains(err.Error(), "weather.txt") {
		t.Fatalf("error should name the file, got %v", err)
	}
}

// Dotfolders (e.g. .obsidian, .git) are skipped by the bare-name search so app
// config files never surface as knowledge-base content.
func TestReadBareNameSkipsDotDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".obsidian/weather.txt", "config")
	r := New(dir)
	if _, _, err := r.Read("weather.txt"); err == nil {
		t.Fatal("file inside dotfolder must not be matched")
	}
}

// A path WITH a slash is an exact lookup — no fuzzy fallback — so a wrong path
// errors instead of silently matching a same-named file elsewhere.
func TestReadRelPathNoFuzzyFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Vault/weather.txt", "sunny")
	r := New(dir)
	if _, _, err := r.Read("wrongdir/weather.txt"); err == nil {
		t.Fatal("explicit relpath must not fall back to fuzzy search")
	}
}

func TestReadDisabled(t *testing.T) {
	r := New("")
	text, e, err := r.Read("anything")
	if text != "" {
		t.Fatalf("want empty text, got %q", text)
	}
	if e != nil {
		t.Fatalf("want nil entry, got %+v", e)
	}
	if err != ErrDisabled {
		t.Fatalf("want ErrDisabled, got %v", err)
	}
}
