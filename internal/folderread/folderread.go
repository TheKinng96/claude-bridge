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
