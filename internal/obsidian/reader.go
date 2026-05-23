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
