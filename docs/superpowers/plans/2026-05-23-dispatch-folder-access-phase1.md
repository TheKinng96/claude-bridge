# Dispatch Folder Access — Phase 1 (Folder Tools + Sonnet) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Telegram/WhatsApp dispatch agent read access to the shared knowledge-base folder (raw browse/read) and the Obsidian vault (wikilink/backlink/tag-aware), and run the dispatcher on sonnet instead of haiku.

**Architecture:** Two new read-only, path-guarded helpers — `internal/folderread` (generic folder browse/read) and an `obsidian.Reader` (note graph) — are wired into the existing `dispatchExecutor`. Five new constrained actions (`list_kb`, `read_kb`, `read_note`, `backlinks`, `search_notes`) are added to the one-shot dispatcher. The dispatcher gets its own sonnet `claude.Client`, leaving the shared haiku client for classification.

**Tech Stack:** Go 1.25, module `claude-bridge`, standard `testing` package, SQLite (unchanged this phase). Spec: `docs/superpowers/specs/2026-05-23-telegram-folder-access-design.md`.

**Note on phasing:** This phase keeps the dispatcher one-shot. Each new action returns its result as the reply (e.g. `read_kb` replies with file content). Chaining `read_kb`→`send_whatsapp` in one instruction is Phase 2 (multi-step loop). Session memory is Phase 3.

---

### Task 1: `folderread` package — `Entry`, `Root`, `List`

**Files:**
- Create: `internal/folderread/folderread.go`
- Test: `internal/folderread/folderread_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/folderread/`
Expected: FAIL — `undefined: New` (package/functions don't exist yet).

- [ ] **Step 3: Write minimal implementation**

```go
// internal/folderread/folderread.go

// Package folderread provides read-only, traversal-guarded access to files
// under a single root directory. It backs the dispatcher's raw knowledge-base
// browse/read actions. It imports nothing from the rest of the app — the
// dispatch Executor wires it in.
package folderread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxReadBytes caps file reads. It is larger than Telegram's 4096-char message
// limit because Read output may be an intermediate loop observation (Phase 2),
// not the final reply; the dispatch layer re-truncates for the user-facing reply.
const MaxReadBytes = 8000

// Entry is one item inside a folder listing.
type Entry struct {
	Name    string
	RelPath string // path relative to Root.Path
	Size    int64
	ModTime time.Time
	IsDir   bool
	IsText  bool
}

// Root anchors all reads at a single directory. Zero-value / empty-path Root is
// disabled — every method returns ErrDisabled, so callers can wire it
// unconditionally.
type Root struct{ Path string }

// New returns a Root for path. Empty path → disabled Root.
func New(path string) *Root {
	if strings.TrimSpace(path) == "" {
		return &Root{}
	}
	return &Root{Path: path}
}

// ErrDisabled signals no folder is configured.
var ErrDisabled = errors.New("folderread: no folder configured")

// Enabled reports whether the root is usable.
func (r *Root) Enabled() bool { return r != nil && r.Path != "" }

// resolve joins rel onto Root.Path and guarantees the result cannot escape the
// root (rejects "..", absolute paths). Returns the cleaned absolute path.
func (r *Root) resolve(rel string) (string, error) {
	// Leading "/" + Clean neutralizes any ".." escape attempts.
	clean := filepath.Clean("/" + strings.TrimSpace(filepath.ToSlash(rel)))
	full := filepath.Join(r.Path, filepath.FromSlash(clean))
	root, err := filepath.Abs(r.Path)
	if err != nil {
		return "", err
	}
	fp, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fp != root && !strings.HasPrefix(fp, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("folderread: path %q escapes root", rel)
	}
	return fp, nil
}

// List returns the entries in Root.Path/subdir, non-recursive. Dotfiles are
// skipped. Directories sort first, then files newest-first. A missing directory
// returns (nil, nil), not an error.
func (r *Root) List(subdir string) ([]Entry, error) {
	if !r.Enabled() {
		return nil, ErrDisabled
	}
	dir, err := r.resolve(subdir)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Entry, 0, len(ents))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    e.Name(),
			RelPath: filepath.ToSlash(filepath.Join(subdir, e.Name())),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
			IsText:  !e.IsDir() && isTextExt(filepath.Ext(e.Name())),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].ModTime.After(out[j].ModTime)
	})
	return out, nil
}

func isTextExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".md", ".markdown", ".txt", ".json", ".yaml", ".yml", ".csv", ".tsv", ".log", ".html", ".xml":
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/folderread/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/folderread/folderread.go internal/folderread/folderread_test.go
git commit -m "feat(folderread): folder listing with dotfile skip + disabled root"
```

---

### Task 2: `folderread.Read` + traversal guard

**Files:**
- Modify: `internal/folderread/folderread.go`
- Modify: `internal/folderread/folderread_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/folderread/folderread_test.go
import "strings" // ensure present at top with other imports

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/folderread/`
Expected: FAIL — `r.Read undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// append to internal/folderread/folderread.go

// Read returns a file's content by path relative to Root.Path. Binary files
// yield a placeholder; text files past MaxReadBytes are truncated. Path escapes
// (via "..") are rejected by resolve.
func (r *Root) Read(rel string) (string, *Entry, error) {
	if !r.Enabled() {
		return "", nil, ErrDisabled
	}
	full, err := r.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", nil, fmt.Errorf("folderread: %w", err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("folderread: %q is a directory", rel)
	}
	e := &Entry{
		Name:    info.Name(),
		RelPath: filepath.ToSlash(rel),
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsText:  isTextExt(filepath.Ext(info.Name())),
	}
	if !e.IsText {
		return fmt.Sprintf("[binary file %s, %d bytes — not shown]", e.Name, e.Size), e, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", e, err
	}
	if len(data) > MaxReadBytes {
		return string(data[:MaxReadBytes]) + "\n…(truncated)", e, nil
	}
	return string(data), e, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/folderread/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/folderread/
git commit -m "feat(folderread): Read with binary placeholder, truncation, traversal guard"
```

---

### Task 3: `obsidian.Reader` — `ReadNote` (frontmatter + links + tags)

**Files:**
- Create: `internal/obsidian/reader.go`
- Create: `internal/obsidian/reader_test.go`

- [ ] **Step 1: Write the failing test**

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/obsidian/ -run TestReadNote`
Expected: FAIL — `undefined: NewReader`.

- [ ] **Step 3: Write minimal implementation**

```go
// internal/obsidian/reader.go

package obsidian

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Reader provides read-only, wikilink/backlink/tag-aware access to an Obsidian
// vault. It complements the write-only Writer in this package. Zero-value /
// empty-path Reader is disabled.
type Reader struct{ VaultPath string }

// NewReader returns a Reader for vaultPath. Empty path → disabled.
func NewReader(vaultPath string) *Reader { return &Reader{VaultPath: vaultPath} }

// Enabled reports whether a vault path is configured.
func (r *Reader) Enabled() bool { return r != nil && r.VaultPath != "" }

// ErrReaderDisabled signals no vault path is configured.
var ErrReaderDisabled = errors.New("obsidian: no vault path configured")

// MaxNoteBytes caps note bodies fed back to the model.
const MaxNoteBytes = 8000

// Note is a parsed vault note.
type Note struct {
	Name        string            // file stem, no extension
	RelPath     string            // path relative to vault
	Frontmatter map[string]string // flat YAML key:value (best-effort)
	Body        string            // note body after frontmatter
	OutLinks    []string          // [[wikilink]] targets (alias/heading stripped)
	Tags        []string          // #tags (without leading #)
}

var (
	wikilinkRE = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	tagRE      = regexp.MustCompile(`(^|\s)#([A-Za-z0-9/_-]+)`)
)

// normalizeName strips [[ ]] wrappers and |alias / #heading suffixes.
func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "[[")
	name = strings.TrimSuffix(name, "]]")
	if i := strings.IndexAny(name, "|#"); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSpace(name)
}

// resolve maps a note name to a concrete .md path inside the vault. Prefers an
// exact relative-path match (e.g. "Clients/Alice"), then a base-name match.
func (r *Reader) resolve(name string) (full, rel string, err error) {
	base := strings.TrimSuffix(normalizeName(name), ".md")
	if base == "" {
		return "", "", errors.New("obsidian: note name required")
	}
	wantStem := strings.ToLower(filepath.Base(base))
	root, _ := filepath.Abs(r.VaultPath)

	var match string
	werr := filepath.WalkDir(r.VaultPath, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rp, _ := filepath.Rel(r.VaultPath, p)
		if strings.EqualFold(strings.TrimSuffix(filepath.ToSlash(rp), ".md"), filepath.ToSlash(base)) {
			match = p
			return filepath.SkipAll
		}
		if match == "" {
			stem := strings.ToLower(strings.TrimSuffix(d.Name(), filepath.Ext(d.Name())))
			if stem == wantStem {
				match = p
			}
		}
		return nil
	})
	if werr != nil {
		return "", "", werr
	}
	if match == "" {
		return "", "", fmt.Errorf("obsidian: no note matching %q", name)
	}
	fp, _ := filepath.Abs(match)
	if fp != root && !strings.HasPrefix(fp, root+string(os.PathSeparator)) {
		return "", "", errors.New("obsidian: note escapes vault")
	}
	rel, _ = filepath.Rel(r.VaultPath, match)
	return match, filepath.ToSlash(rel), nil
}

// ReadNote resolves name and parses frontmatter, body, outgoing links, and tags.
func (r *Reader) ReadNote(name string) (*Note, error) {
	if !r.Enabled() {
		return nil, ErrReaderDisabled
	}
	full, rel, err := r.resolve(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("obsidian: %w", err)
	}
	fm, body := splitFrontmatter(string(data))
	n := &Note{
		Name:        strings.TrimSuffix(filepath.Base(rel), ".md"),
		RelPath:     rel,
		Frontmatter: fm,
		OutLinks:    extractLinks(body),
		Tags:        extractTags(body),
	}
	if len(body) > MaxNoteBytes {
		body = body[:MaxNoteBytes] + "\n…(truncated)"
	}
	n.Body = body
	return n, nil
}

// splitFrontmatter separates a leading "---\n...\n---" YAML block from the body.
// Returns flat key:value pairs (best-effort; nested YAML ignored) and the body.
func splitFrontmatter(s string) (map[string]string, string) {
	fm := map[string]string{}
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return fm, s
	}
	nl := strings.Index(s, "\n")
	rest := s[nl+1:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, s
	}
	block := rest[:end]
	body := strings.TrimPrefix(rest[end:], "\n---")
	body = strings.TrimLeft(body, "\r\n")
	for _, line := range strings.Split(block, "\n") {
		if i := strings.Index(line, ":"); i > 0 {
			fm[strings.TrimSpace(line[:i])] = strings.TrimSpace(line[i+1:])
		}
	}
	return fm, body
}

func extractLinks(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range wikilinkRE.FindAllStringSubmatch(body, -1) {
		l := normalizeName(m[1])
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func extractTags(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range tagRE.FindAllStringSubmatch(body, -1) {
		if t := m[2]; t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func trimSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

var _ = sort.Strings // used by Backlinks (Task 4)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/obsidian/ -run TestReadNote`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/obsidian/reader.go internal/obsidian/reader_test.go
git commit -m "feat(obsidian): Reader.ReadNote with frontmatter/wikilink/tag parsing"
```

---

### Task 4: `obsidian.Reader.Backlinks`

**Files:**
- Modify: `internal/obsidian/reader.go`
- Modify: `internal/obsidian/reader_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/obsidian/reader_test.go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/obsidian/ -run TestBacklinks`
Expected: FAIL — `r.Backlinks undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// append to internal/obsidian/reader.go

// Backlinks returns vault notes (by relative stem, e.g. "Clients/Alice") that
// contain a [[wikilink]] to name. On-demand scan — fine for a personal vault.
func (r *Reader) Backlinks(name string) ([]string, error) {
	if !r.Enabled() {
		return nil, ErrReaderDisabled
	}
	target := strings.ToLower(normalizeName(name))
	if target == "" {
		return nil, errors.New("obsidian: note name required")
	}
	seen := map[string]bool{}
	var out []string
	_ = filepath.WalkDir(r.VaultPath, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range wikilinkRE.FindAllStringSubmatch(string(data), -1) {
			l := strings.ToLower(normalizeName(m[1]))
			if l == target || strings.EqualFold(filepath.Base(l), target) {
				rel, _ := filepath.Rel(r.VaultPath, p)
				key := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
				if !seen[key] {
					seen[key] = true
					out = append(out, key)
				}
				break
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, nil
}
```

Then remove the now-unneeded `var _ = sort.Strings` line added in Task 3.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/obsidian/`
Expected: PASS (all obsidian tests).

- [ ] **Step 5: Commit**

```bash
git add internal/obsidian/
git commit -m "feat(obsidian): Reader.Backlinks via vault scan"
```

---

### Task 5: `obsidian.Reader.Search` (text + tag)

**Files:**
- Modify: `internal/obsidian/reader.go`
- Modify: `internal/obsidian/reader_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/obsidian/reader_test.go
func TestSearchText(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "A.md", "renewal due in March\n")
	writeNote(t, vault, "B.md", "nothing here\n")
	r := NewReader(vault)
	hits, err := r.Search("renewal", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Note != "A" {
		t.Fatalf("unexpected hits %+v", hits)
	}
}

func TestSearchTag(t *testing.T) {
	vault := t.TempDir()
	writeNote(t, vault, "A.md", "policy stuff #vip\n")
	writeNote(t, vault, "B.md", "no tag\n")
	r := NewReader(vault)
	hits, err := r.Search("", "vip")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Note != "A" {
		t.Fatalf("unexpected hits %+v", hits)
	}
}

func TestSearchRequiresInput(t *testing.T) {
	r := NewReader(t.TempDir())
	if _, err := r.Search("", ""); err == nil {
		t.Fatal("expected error when query and tag both empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/obsidian/ -run TestSearch`
Expected: FAIL — `r.Search undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// append to internal/obsidian/reader.go

// Hit is one search match.
type Hit struct {
	Note    string // relative stem, e.g. "Clients/Alice"
	Line    int    // 1-based; 0 when matched by tag only
	Snippet string
}

// Search scans vault notes. With query set, returns the first matching line per
// note (case-insensitive substring). With tag set, restricts to notes carrying
// that #tag. At least one of query/tag is required. Capped at 20 hits.
func (r *Reader) Search(query, tag string) ([]Hit, error) {
	if !r.Enabled() {
		return nil, ErrReaderDisabled
	}
	q := strings.ToLower(strings.TrimSpace(query))
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "#")
	if q == "" && tag == "" {
		return nil, errors.New("obsidian: query or tag required")
	}
	const maxHits = 20
	var hits []Hit
	_ = filepath.WalkDir(r.VaultPath, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		text := string(data)
		if tag != "" {
			ok := false
			for _, tg := range extractTags(text) {
				if strings.EqualFold(tg, tag) {
					ok = true
					break
				}
			}
			if !ok {
				return nil
			}
		}
		rel, _ := filepath.Rel(r.VaultPath, p)
		note := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		if q == "" {
			hits = append(hits, Hit{Note: note, Line: 0, Snippet: trimSnippet(text)})
		} else {
			for i, ln := range strings.Split(text, "\n") {
				if strings.Contains(strings.ToLower(ln), q) {
					hits = append(hits, Hit{Note: note, Line: i + 1, Snippet: trimSnippet(ln)})
					break
				}
			}
		}
		if len(hits) >= maxHits {
			return filepath.SkipAll
		}
		return nil
	})
	return hits, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/obsidian/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/obsidian/
git commit -m "feat(obsidian): Reader.Search by text and tag"
```

---

### Task 6: Agent package — new actions, view types, Executor methods, prompt

**Files:**
- Modify: `internal/agent/dispatch.go`
- Modify: `internal/agent/dispatch_test.go` (extend `fakeExec`)

- [ ] **Step 1: Write the failing test**

```go
// append to internal/agent/dispatch_test.go

func TestDispatchListKB(t *testing.T) {
	reply := `{"action":"list_kb","params":{"subdir":""},"user_reply":"Listing your KB folder."}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.kbEntries = []KBEntry{{Name: "report.md", IsText: true}, {Name: "imgs", IsDir: true}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "list my kb"})
	if res.Action != ActionListKB {
		t.Fatalf("want list_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "report.md") {
		t.Fatalf("reply should list files, got %q", res.UserReply)
	}
}

func TestDispatchReadKB(t *testing.T) {
	reply := `{"action":"read_kb","params":{"path":"report.md"},"user_reply":"Here it is:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.kbContent = "Q1 revenue up 12%."
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read report.md"})
	if res.Action != ActionReadKB {
		t.Fatalf("want read_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "Q1 revenue") {
		t.Fatalf("reply should contain file content, got %q", res.UserReply)
	}
}

func TestDispatchReadNote(t *testing.T) {
	reply := `{"action":"read_note","params":{"name":"Alice"},"user_reply":"Note:"}`
	d, _, ex, _ := newTestDispatcher(reply)
	ex.note = &NoteView{Name: "Alice", Body: "VIP client", OutLinks: []string{"Tan Policy"}, Tags: []string{"vip"}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "open Alice"})
	if res.Action != ActionReadNote || !strings.Contains(res.UserReply, "VIP client") {
		t.Fatalf("unexpected result %s / %q", res.Action, res.UserReply)
	}
}
```

Also extend the `fakeExec` struct and add the new method stubs:

```go
// in internal/agent/dispatch_test.go — add fields to the fakeExec struct:
//   kbEntries []KBEntry
//   kbContent string
//   note      *NoteView
//   backlinks []string
//   noteHits  []NoteHit

func (f *fakeExec) ListKB(ctx context.Context, subdir string) ([]KBEntry, error) {
	return f.kbEntries, nil
}
func (f *fakeExec) ReadKB(ctx context.Context, path string) (string, *KBEntry, error) {
	return f.kbContent, &KBEntry{Name: path, IsText: true}, nil
}
func (f *fakeExec) ReadNote(ctx context.Context, name string) (*NoteView, error) {
	return f.note, nil
}
func (f *fakeExec) Backlinks(ctx context.Context, name string) ([]string, error) {
	return f.backlinks, nil
}
func (f *fakeExec) SearchNotes(ctx context.Context, query, tag string) ([]NoteHit, error) {
	return f.noteHits, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestDispatch(ListKB|ReadKB|ReadNote)'`
Expected: FAIL — undefined `ActionListKB`, `KBEntry`, `NoteView`, `NoteHit`, and `fakeExec` missing interface methods.

- [ ] **Step 3: Write minimal implementation**

Add the action consts to the `const (...)` block in `internal/agent/dispatch.go`:

```go
	ActionListKB      Action = "list_kb"
	ActionReadKB      Action = "read_kb"
	ActionReadNote    Action = "read_note"
	ActionBacklinks   Action = "backlinks"
	ActionSearchNotes Action = "search_notes"
```

Add the view types near the other view structs (e.g. after `CoworkRead`):

```go
// KBEntry is one file/dir in the raw knowledge-base folder.
type KBEntry struct {
	Name    string
	RelPath string
	Size    int64
	IsDir   bool
	IsText  bool
}

// NoteView is a flattened Obsidian note for dispatch output.
type NoteView struct {
	Name        string
	RelPath     string
	Frontmatter map[string]string
	Body        string
	OutLinks    []string
	Tags        []string
}

// NoteHit is one Obsidian search match.
type NoteHit struct {
	Note    string
	Line    int
	Snippet string
}
```

Add to the `Executor` interface:

```go
	ListKB(ctx context.Context, subdir string) ([]KBEntry, error)
	ReadKB(ctx context.Context, path string) (string, *KBEntry, error)
	ReadNote(ctx context.Context, name string) (*NoteView, error)
	Backlinks(ctx context.Context, name string) ([]string, error)
	SearchNotes(ctx context.Context, query, tag string) ([]NoteHit, error)
```

Add cases to `execute`'s switch (before `default`/end):

```go
	case ActionListKB:
		var args struct {
			Subdir string `json:"subdir"`
		}
		_ = json.Unmarshal(p.Params, &args)
		entries, err := d.Exec.ListKB(ctx, args.Subdir)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "(folder empty or not configured)", nil
		}
		var sb strings.Builder
		for i, e := range entries {
			if i >= 30 {
				fmt.Fprintf(&sb, "\n…(+%d more)", len(entries)-30)
				break
			}
			kind := "📄"
			if e.IsDir {
				kind = "📁"
			}
			fmt.Fprintf(&sb, "\n%s %s", kind, e.RelPath)
		}
		return strings.TrimSpace(sb.String()), nil

	case ActionReadKB:
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("read_kb params: %w", err)
		}
		if strings.TrimSpace(args.Path) == "" {
			return "", errors.New("read_kb: path required")
		}
		content, _, err := d.Exec.ReadKB(ctx, args.Path)
		if err != nil {
			return "", err
		}
		return truncate(content, 3500), nil

	case ActionReadNote:
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("read_note params: %w", err)
		}
		note, err := d.Exec.ReadNote(ctx, args.Name)
		if err != nil {
			return "", err
		}
		if note == nil {
			return "(note not found)", nil
		}
		var sb strings.Builder
		sb.WriteString(note.Body)
		if len(note.OutLinks) > 0 {
			fmt.Fprintf(&sb, "\n\nLinks: %s", strings.Join(note.OutLinks, ", "))
		}
		if len(note.Tags) > 0 {
			fmt.Fprintf(&sb, "\nTags: #%s", strings.Join(note.Tags, " #"))
		}
		return truncate(strings.TrimSpace(sb.String()), 3500), nil

	case ActionBacklinks:
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("backlinks params: %w", err)
		}
		links, err := d.Exec.Backlinks(ctx, args.Name)
		if err != nil {
			return "", err
		}
		if len(links) == 0 {
			return "(no backlinks)", nil
		}
		return "Linked from: " + strings.Join(links, ", "), nil

	case ActionSearchNotes:
		var args struct {
			Query string `json:"query"`
			Tag   string `json:"tag"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("search_notes params: %w", err)
		}
		hits, err := d.Exec.SearchNotes(ctx, args.Query, args.Tag)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matching notes)", nil
		}
		var sb strings.Builder
		for i, h := range hits {
			if i >= 15 {
				break
			}
			fmt.Fprintf(&sb, "\n• %s: %s", h.Note, h.Snippet)
		}
		return strings.TrimSpace(sb.String()), nil
```

Update the `dispatchSystemPrompt` action enum and add catalog entries. In the schema line add the five actions, and append to the "Action params:" list:

```
- list_kb: {"subdir": ""} — list files/folders in the shared knowledge-base folder. Empty subdir lists the top level; pass a relative subfolder to drill in.
- read_kb: {"path": "report.md" | "sub/report.md"} — read a file from the knowledge-base folder by its relative path (use list_kb first to get paths). Binary files return a placeholder.
- read_note: {"name": "Alice" | "Clients/Alice" | "[[Alice]]"} — read an Obsidian note: body plus its outgoing [[links]] and #tags.
- backlinks: {"name": "Tan Policy"} — list notes that link TO the named note.
- search_notes: {"query": "renewal", "tag": "vip"} — search Obsidian notes by text and/or #tag (at least one). Empty query with a tag lists all notes carrying that tag.
```

Update `actionCatalog` to include: `list_kb, read_kb, read_note, backlinks, search_notes`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/`
Expected: PASS (new tests + existing dispatch tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(agent): add list_kb/read_kb/read_note/backlinks/search_notes actions"
```

---

### Task 7: Wire executor + sonnet client in `main.go`

**Files:**
- Modify: `main.go` (`dispatchExecutor` struct ~line 70; methods after `EditCowork` ~line 420; dispatcher build ~line 587-602)

- [ ] **Step 1: Add fields + methods to `dispatchExecutor`**

Add imports `claude-bridge/internal/folderread` and ensure `claude-bridge/internal/obsidian` is imported. Add fields to the `dispatchExecutor` struct:

```go
	vaultPath string // resolved per-call freshness handled via store reload below
```

(Existing fields `store`, `obsidian`, `cowork` stay.) Then add the methods near `EditCowork`:

```go
// kbRoot builds a folderread.Root from the current knowledge config so dashboard
// folder changes take effect without a restart.
func (e *dispatchExecutor) kbRoot(ctx context.Context) *folderread.Root {
	cfg, _ := knowledge.LoadConfig(ctx, e.store)
	return folderread.New(cfg.FolderPath)
}

// vaultReader builds an obsidian.Reader from the current agent config.
func (e *dispatchExecutor) vaultReader(ctx context.Context) *obsidian.Reader {
	cfg, _ := agent.LoadConfig(ctx, e.store)
	return obsidian.NewReader(cfg.ObsidianVaultPath)
}

func (e *dispatchExecutor) ListKB(ctx context.Context, subdir string) ([]agent.KBEntry, error) {
	r := e.kbRoot(ctx)
	if !r.Enabled() {
		return nil, fmt.Errorf("set a knowledge base folder on the Dashboard first")
	}
	rows, err := r.List(subdir)
	if err != nil {
		return nil, err
	}
	out := make([]agent.KBEntry, 0, len(rows))
	for _, x := range rows {
		out = append(out, agent.KBEntry{Name: x.Name, RelPath: x.RelPath, Size: x.Size, IsDir: x.IsDir, IsText: x.IsText})
	}
	return out, nil
}

func (e *dispatchExecutor) ReadKB(ctx context.Context, path string) (string, *agent.KBEntry, error) {
	r := e.kbRoot(ctx)
	if !r.Enabled() {
		return "", nil, fmt.Errorf("set a knowledge base folder on the Dashboard first")
	}
	text, ent, err := r.Read(path)
	if err != nil {
		return "", nil, err
	}
	var ve *agent.KBEntry
	if ent != nil {
		ve = &agent.KBEntry{Name: ent.Name, RelPath: ent.RelPath, Size: ent.Size, IsText: ent.IsText}
	}
	return text, ve, nil
}

func (e *dispatchExecutor) ReadNote(ctx context.Context, name string) (*agent.NoteView, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set an Obsidian vault path on the Dashboard first")
	}
	n, err := rd.ReadNote(name)
	if err != nil {
		return nil, err
	}
	return &agent.NoteView{
		Name: n.Name, RelPath: n.RelPath, Frontmatter: n.Frontmatter,
		Body: n.Body, OutLinks: n.OutLinks, Tags: n.Tags,
	}, nil
}

func (e *dispatchExecutor) Backlinks(ctx context.Context, name string) ([]string, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set an Obsidian vault path on the Dashboard first")
	}
	return rd.Backlinks(name)
}

func (e *dispatchExecutor) SearchNotes(ctx context.Context, query, tag string) ([]agent.NoteHit, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set an Obsidian vault path on the Dashboard first")
	}
	hits, err := rd.Search(query, tag)
	if err != nil {
		return nil, err
	}
	out := make([]agent.NoteHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.NoteHit{Note: h.Note, Line: h.Line, Snippet: h.Snippet})
	}
	return out, nil
}
```

- [ ] **Step 2: Build to verify the executor satisfies the interface**

Run: `go build ./...`
Expected: SUCCESS (the new methods make `*dispatchExecutor` satisfy the expanded `agent.Executor`).

- [ ] **Step 3: Add the sonnet dispatch client**

In `main.go`, just before the `dispatcher := agent.NewDispatcher(` call (~line 591), add:

```go
	// Dispatcher runs on sonnet for better action selection + replies. The
	// shared knowClient stays on haiku for bulk classification/auto-reply.
	dispatchClient := claude.New("", "claude-sonnet-4-6")
```

Then change the first argument of `NewDispatcher` from `knowClient` to `dispatchClient`.

- [ ] **Step 4: Build + run the full test suite**

Run: `go build ./... && go test ./...`
Expected: SUCCESS; all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat(dispatch): wire KB folder + Obsidian reader executors; run dispatcher on sonnet"
```

---

### Task 8: Manual smoke test (folder read, one-shot)

**Files:** none (manual verification).

- [ ] **Step 1: Build the binary**

Run: `go build -o claude-bridge .`
Expected: SUCCESS.

- [ ] **Step 2: Verify the action surface compiles into the prompt**

Run: `grep -c "list_kb\|read_kb\|read_note\|backlinks\|search_notes" internal/agent/dispatch.go`
Expected: ≥ 2 (consts + prompt/catalog references present).

- [ ] **Step 3: Live check (requires a configured KB folder + Telegram bot)**

Start the app, set a knowledge-base folder on the Dashboard (`/setup/knowledge`) containing a small `.md` file, then message the bot: `list my knowledge folder`, then `read <that file>`. Confirm the bot lists files and returns content within one reply each.

Note: chaining ("read X and message Alice about it") is **not** expected to work yet — that is Phase 2 (multi-step loop). This phase delivers folder reading only.

- [ ] **Step 4: Commit (if any prompt tweaks were needed)**

```bash
git add -A && git commit -m "chore(dispatch): phase-1 prompt tweaks from smoke test"
```

(Skip if no changes.)

---

## Self-Review Notes

- **Spec coverage (Phase 1 scope):** raw KB `list_kb`/`read_kb` (Tasks 1-2, 6-7) ✓;
  Obsidian-aware `read_note`/`backlinks`/`search_notes` (Tasks 3-5, 6-7) ✓;
  per-call path resolution for runtime dashboard edits (Task 7 `kbRoot`/`vaultReader`) ✓;
  sonnet dispatch client separate from haiku (Task 7) ✓; traversal guard + binary
  placeholder + truncation (Tasks 1-2, mirrored in obsidian) ✓.
- **Deferred to later phases (intentionally not here):** multi-step loop / action
  chaining (Phase 2); `recall_memory`, `dispatch_sessions`, `SessionCompactor`
  (Phase 3). The Executor interface is extended only with the five read methods this
  phase; `RecallMemory` is added in Phase 3.
- **Type consistency:** `KBEntry`/`NoteView`/`NoteHit` defined in Task 6 are used
  verbatim in Tasks 6-7; `folderread.Entry`/`obsidian.Note`/`obsidian.Hit` are mapped
  to them in the executor. `NewReader` (obsidian) vs `New` (folderread) names are
  distinct and used consistently.
- **Known follow-up:** extending the `Executor` interface breaks any other
  implementer; the only ones are `*dispatchExecutor` (Task 7) and `fakeExec` (Task 6).
  Both are updated here.
