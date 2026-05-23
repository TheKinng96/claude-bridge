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
	// filepath.Abs is lexical and does not follow symlinks; a .md symlink inside
	// the vault pointing outside it would otherwise be followed by ReadFile.
	// Resolve symlinks and re-check the prefix. Resolve the root too, since the
	// root path itself may traverse symlinks (e.g. /var -> /private/var on macOS).
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("obsidian: %w", err)
		}
		return "", "", err
	}
	real, err := filepath.EvalSymlinks(fp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("obsidian: %w", err)
		}
		return "", "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
		return "", "", errors.New("obsidian: note escapes vault via symlink")
	}
	rel, _ = filepath.Rel(r.VaultPath, match)
	return real, filepath.ToSlash(rel), nil
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
	// The closing fence must be a full line: "\n---" followed by a newline or the
	// end of the string. Scan candidate "\n---" positions and accept only the
	// first one that is line-anchored, so a setext heading or a YAML scalar line
	// beginning with "---" inside the block isn't mistaken for the fence.
	end := -1
	for off := 0; ; {
		i := strings.Index(rest[off:], "\n---")
		if i < 0 {
			break
		}
		pos := off + i
		after := rest[pos+len("\n---"):]
		if after == "" || strings.HasPrefix(after, "\n") || strings.HasPrefix(after, "\r\n") {
			end = pos
			break
		}
		off = pos + len("\n---")
	}
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
	if werr := filepath.WalkDir(r.VaultPath, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		// Skip symlinks so a symlinked .md can't leak outside content into results.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		text := string(data)
		// Tag matching uses the body only — a YAML comment ("# foo") in the
		// frontmatter must not be mistaken for a #tag.
		_, body := splitFrontmatter(text)
		if tag != "" {
			ok := false
			for _, tg := range extractTags(body) {
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
	}); werr != nil {
		return nil, werr
	}
	return hits, nil
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
	targetStem := filepath.Base(target)
	seen := map[string]bool{}
	var out []string
	if werr := filepath.WalkDir(r.VaultPath, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		// Skip symlinks so a symlinked .md can't leak outside content into results.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range wikilinkRE.FindAllStringSubmatch(string(data), -1) {
			l := strings.ToLower(normalizeName(m[1]))
			// Match full-name, link-stem-vs-target, and stem-vs-stem so a
			// path-qualified query ("Clients/Tan Policy") still finds short-form
			// links ("[[Tan Policy]]") and vice versa.
			if l == target ||
				strings.EqualFold(filepath.Base(l), target) ||
				strings.EqualFold(filepath.Base(l), targetStem) {
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
	}); werr != nil {
		return nil, werr
	}
	sort.Strings(out)
	return out, nil
}
