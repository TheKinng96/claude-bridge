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

	"claude-bridge/internal/docextract"
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
// root. It rejects absolute paths and any ".." component up front (rather than
// silently remapping them), then applies a lexical prefix check and a
// symlink-resolved prefix check as defense-in-depth. Returns the resolved path.
func (r *Root) resolve(rel string) (string, error) {
	slash := strings.TrimSpace(filepath.ToSlash(rel))
	// Reject absolute paths outright — they would escape the root.
	if filepath.IsAbs(rel) || strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("folderread: absolute path not allowed: %q", rel)
	}
	// Reject any ".." component instead of letting Clean remap it into root.
	for _, part := range strings.Split(slash, "/") {
		if part == ".." {
			return "", fmt.Errorf("folderread: path %q escapes root", rel)
		}
	}
	clean := filepath.Clean("/" + slash)
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
	// filepath.Abs is lexical and does not follow symlinks; a symlink inside the
	// root pointing outside it would otherwise be followed by Stat/ReadFile.
	// Resolve symlinks and re-check the prefix. Resolve the root too, since the
	// root path itself may traverse symlinks (e.g. /var -> /private/var on macOS).
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(fp)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fp, nil // not-yet-existing path; lexical guard suffices, Stat/ReadFile will surface ErrNotExist
		}
		return "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("folderread: path %q escapes root via symlink", rel)
	}
	return real, nil
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
		// Bare filename that isn't at the root: search the whole folder for a
		// file of that name. The KB folder is the superset of the Vault and
		// Cowork subfolders, so the owner can name a file without its path.
		if errors.Is(err, os.ErrNotExist) && isBareName(rel) {
			if found, ok := r.findByName(rel); ok {
				return r.Read(found)
			}
			return "", nil, fmt.Errorf("folderread: no file named %q found in the knowledge folder", rel)
		}
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
		if docextract.Supported(full) {
			text, ok, err := docextract.Extract(full)
			if err != nil {
				return fmt.Sprintf("[%s: extraction failed — %v]", e.Name, err), e, nil
			}
			if ok {
				return text, e, nil
			}
		}
		return fmt.Sprintf("[binary file %s, %d bytes — not shown]", e.Name, e.Size), e, nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", e, err
	}
	if len(data) > MaxReadBytes {
		// Byte-based truncation; may split a multi-byte UTF-8 sequence at the
		// boundary (matches cowork behavior; not a regression).
		return string(data[:MaxReadBytes]) + "\n…(truncated)", e, nil
	}
	return string(data), e, nil
}

// isBareName reports whether rel is a single filename with no path separator —
// the only shape that triggers Read's folder-wide search fallback. Paths with a
// slash are treated as exact lookups.
func isBareName(rel string) bool {
	s := strings.TrimSpace(filepath.ToSlash(rel))
	return s != "" && !strings.Contains(s, "/")
}

// findByName walks the folder for a file whose name matches the bare query
// (case-insensitive). An exact basename match wins; if the query has no
// extension, a stem match ("weather" → "weather.txt") is accepted too. On
// multiple matches the newest file wins. Dotfolders, dotfiles and symlinks are
// skipped so app config (.obsidian) and symlinked files never leak in. Returns
// a slash relpath suitable for Read, and whether anything matched.
func (r *Root) findByName(name string) (string, bool) {
	target := strings.ToLower(strings.TrimSpace(filepath.ToSlash(name)))
	targetStem := strings.TrimSuffix(target, filepath.Ext(target))
	hasExt := filepath.Ext(target) != ""

	var bestRel string
	var bestMod time.Time
	_ = filepath.WalkDir(r.Path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		base := d.Name()
		if d.IsDir() {
			if p != r.Path && strings.HasPrefix(base, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(base, ".") || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		lb := strings.ToLower(base)
		match := lb == target
		if !match && !hasExt {
			match = strings.TrimSuffix(lb, filepath.Ext(lb)) == targetStem
		}
		if !match {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if bestRel == "" || info.ModTime().After(bestMod) {
			rel, rerr := filepath.Rel(r.Path, p)
			if rerr != nil {
				return nil
			}
			bestRel = filepath.ToSlash(rel)
			bestMod = info.ModTime()
		}
		return nil
	})
	return bestRel, bestRel != ""
}
