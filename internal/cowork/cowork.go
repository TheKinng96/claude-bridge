// Package cowork exposes the dated cowork folder (<vault>/Cowork/YYYY-MM-DD/)
// to the dispatcher so the owner can list, read, search, and edit files
// produced by routines — markdown notes, generated images, JSON dumps, PDFs.
//
// The package is filesystem-only. It does not import the rest of the app; the
// dispatcher executor wires it in via the Executor interface. Binary files
// (images, PDFs) are surfaced for listing/searching by name only — text
// editing is restricted to text-friendly extensions.
package cowork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// FileEntry is one file inside a date folder.
type FileEntry struct {
	Name    string
	Date    string // YYYY-MM-DD folder it lives in
	Path    string // absolute path on disk
	Size    int64
	ModTime time.Time
	IsText  bool
}

// SearchHit is one grep match inside a text file.
type SearchHit struct {
	Date    string
	Name    string
	Line    int
	Snippet string
}

// Root anchors all cowork operations at <vault>/Cowork/.
type Root struct {
	Path string
	// Now is overridable for tests; defaults to time.Now.
	Now func() time.Time
}

// New builds a Root for the given vault. Empty vaultPath returns a disabled
// Root whose methods all return ErrDisabled — callers can wire unconditionally.
func New(vaultPath string) *Root {
	if vaultPath == "" {
		return &Root{}
	}
	return &Root{Path: filepath.Join(vaultPath, "Cowork")}
}

// ErrDisabled signals the root has no vault path configured.
var ErrDisabled = errors.New("cowork: vault path not configured")

// Enabled reports whether the root is usable.
func (r *Root) Enabled() bool { return r != nil && r.Path != "" }

func (r *Root) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// DateFolder is the absolute path of the dated folder for t. It does not
// create the directory.
func (r *Root) DateFolder(t time.Time) string {
	return filepath.Join(r.Path, t.Format("2006-01-02"))
}

// EnsureToday creates today's date folder (and the Cowork root) and returns
// its absolute path. External routines call this — via the cowork_path
// dispatch action or the get_cowork_folder MCP tool — to learn exactly where
// to write so the file is the same one Telegram lists/reads/edits.
func (r *Root) EnsureToday() (string, error) {
	return r.EnsureDate("")
}

// EnsureDate creates the date folder for date ("", "today", "yesterday", or
// YYYY-MM-DD) and returns its absolute path.
func (r *Root) EnsureDate(date string) (string, error) {
	if !r.Enabled() {
		return "", ErrDisabled
	}
	t, err := r.ResolveDate(date)
	if err != nil {
		return "", fmt.Errorf("cowork: parse date %q: %w", date, err)
	}
	dir := r.DateFolder(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ResolveDate accepts "", "today", "yesterday", or YYYY-MM-DD.
func (r *Root) ResolveDate(s string) (time.Time, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	now := r.now()
	switch s {
	case "", "today":
		return now, nil
	case "yesterday":
		return now.AddDate(0, 0, -1), nil
	}
	return time.Parse("2006-01-02", s)
}

// List returns files in the date folder, newest first. Missing folder returns
// (nil, nil) — not an error, just empty.
func (r *Root) List(date string) ([]FileEntry, error) {
	if !r.Enabled() {
		return nil, ErrDisabled
	}
	t, err := r.ResolveDate(date)
	if err != nil {
		return nil, fmt.Errorf("cowork: parse date %q: %w", date, err)
	}
	return r.listFolder(t)
}

func (r *Root) listFolder(t time.Time) ([]FileEntry, error) {
	dir := r.DateFolder(t)
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	date := t.Format("2006-01-02")
	out := make([]FileEntry, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{
			Name:    e.Name(),
			Date:    date,
			Path:    filepath.Join(dir, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsText:  isTextExt(filepath.Ext(e.Name())),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// MaxReadBytes caps text reads so the dispatcher reply stays within Telegram's
// 4096-character message limit (with headroom for the surrounding wrapper).
const MaxReadBytes = 3500

// Read returns the file content. Binary files yield a placeholder; text files
// are truncated past MaxReadBytes.
//
// Filename accepts: bare name (fuzzy match across the last 14 days), or
// "YYYY-MM-DD/name" for an exact lookup.
func (r *Root) Read(filename string) (text string, entry *FileEntry, err error) {
	if !r.Enabled() {
		return "", nil, ErrDisabled
	}
	entry, err = r.Find(filename)
	if err != nil {
		return "", nil, err
	}
	if !entry.IsText {
		return fmt.Sprintf("[binary file %s, %d bytes — open via Obsidian or filesystem]", entry.Name, entry.Size), entry, nil
	}
	data, err := os.ReadFile(entry.Path)
	if err != nil {
		return "", entry, err
	}
	if len(data) > MaxReadBytes {
		return string(data[:MaxReadBytes]) + "\n…(truncated)", entry, nil
	}
	return string(data), entry, nil
}

// Search greps text files across the last `days` date folders (caller passes
// 0 to use the default of 7; capped at 30).
func (r *Root) Search(query string, days int) ([]SearchHit, error) {
	if !r.Enabled() {
		return nil, ErrDisabled
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, errors.New("cowork: empty search query")
	}
	if days <= 0 {
		days = 7
	}
	if days > 30 {
		days = 30
	}
	needle := strings.ToLower(q)
	const maxHits = 20
	var hits []SearchHit
	now := r.now()
	for d := range days {
		date := now.AddDate(0, 0, -d)
		dir := r.DateFolder(date)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		dateStr := date.Format("2006-01-02")
		for _, e := range ents {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if !isTextExt(filepath.Ext(e.Name())) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for i, ln := range strings.Split(string(data), "\n") {
				if strings.Contains(strings.ToLower(ln), needle) {
					hits = append(hits, SearchHit{
						Date:    dateStr,
						Name:    e.Name(),
						Line:    i + 1,
						Snippet: trimSnippet(ln),
					})
					if len(hits) >= maxHits {
						return hits, nil
					}
				}
			}
		}
	}
	return hits, nil
}

// EditOp is the kind of in-place modification supported by Edit.
type EditOp string

const (
	OpAppend  EditOp = "append"
	OpReplace EditOp = "replace"
)

// Edit modifies a text file in place. op="append" adds content as a new line
// (or block); op="replace" overwrites the whole file. Binary files reject.
func (r *Root) Edit(filename, content string, op EditOp) (*FileEntry, error) {
	if !r.Enabled() {
		return nil, ErrDisabled
	}
	entry, err := r.Find(filename)
	if err != nil {
		return nil, err
	}
	if !entry.IsText {
		return nil, fmt.Errorf("cowork: cannot edit binary file %s", entry.Name)
	}
	switch op {
	case "", OpAppend:
		f, err := os.OpenFile(entry.Path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err := f.WriteString("\n" + content); err != nil {
			return nil, err
		}
	case OpReplace:
		if err := os.WriteFile(entry.Path, []byte(content), 0o644); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("cowork: unknown edit op %q (use append or replace)", op)
	}
	if info, err := os.Stat(entry.Path); err == nil {
		entry.Size = info.Size()
		entry.ModTime = info.ModTime()
	}
	return entry, nil
}

// WriteOutput creates a new file inside today's folder. routine is a short
// label (e.g. "broadcast"); slug is a per-call identifier; ext should include
// the dot (".md", ".png", ".json"). Returns the full path written.
//
// Filenames take the form <routine>_<slug>_<HHMMSS>.<ext> so multiple writes
// in the same second/slug don't collide.
func (r *Root) WriteOutput(routine, slug, ext string, data []byte) (string, error) {
	if !r.Enabled() {
		return "", ErrDisabled
	}
	t := r.now()
	dir := r.DateFolder(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s_%s_%s%s", safeSlug(routine), safeSlug(slug), t.Format("150405"), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Find resolves a filename hint to a concrete file entry. Two forms:
//
//   - "YYYY-MM-DD/name"  → exact path.
//   - "name"             → newest fuzzy (case-insensitive substring) match in
//     the last 14 days.
//
// Leading "Cowork/" or "cowork/" prefixes (e.g. when the owner copies a path
// from Obsidian) are stripped.
func (r *Root) Find(filename string) (*FileEntry, error) {
	if !r.Enabled() {
		return nil, ErrDisabled
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, errors.New("cowork: filename required")
	}
	filename = strings.TrimPrefix(filename, "Cowork/")
	filename = strings.TrimPrefix(filename, "cowork/")

	if i := strings.Index(filename, "/"); i > 0 {
		if t, err := time.Parse("2006-01-02", filename[:i]); err == nil {
			name := filename[i+1:]
			full := filepath.Join(r.Path, filename[:i], name)
			info, err := os.Stat(full)
			if err != nil {
				return nil, fmt.Errorf("cowork: %w", err)
			}
			return &FileEntry{
				Name:    name,
				Date:    t.Format("2006-01-02"),
				Path:    full,
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsText:  isTextExt(filepath.Ext(name)),
			}, nil
		}
	}

	needle := strings.ToLower(filename)
	var newest *FileEntry
	now := r.now()
	for d := range 14 {
		date := now.AddDate(0, 0, -d)
		ents, err := os.ReadDir(r.DateFolder(date))
		if err != nil {
			continue
		}
		dateStr := date.Format("2006-01-02")
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			lname := strings.ToLower(e.Name())
			if lname != needle && !strings.Contains(lname, needle) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			cand := &FileEntry{
				Name:    e.Name(),
				Date:    dateStr,
				Path:    filepath.Join(r.DateFolder(date), e.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsText:  isTextExt(filepath.Ext(e.Name())),
			}
			// Exact (case-insensitive) match short-circuits — caller named the
			// file precisely, so don't drift to a fuzzy substring match from a
			// newer date.
			if lname == needle {
				return cand, nil
			}
			if newest == nil || cand.ModTime.After(newest.ModTime) {
				newest = cand
			}
		}
	}
	if newest == nil {
		return nil, fmt.Errorf("cowork: no file matching %q in last 14 days", filename)
	}
	return newest, nil
}

func isTextExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".md", ".markdown", ".txt", ".json", ".yaml", ".yml", ".csv", ".log", ".html":
		return true
	}
	return false
}

var unsafeSlugRE = regexp.MustCompile(`[^a-zA-Z0-9-]+`)

func safeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = unsafeSlugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "out"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func trimSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
