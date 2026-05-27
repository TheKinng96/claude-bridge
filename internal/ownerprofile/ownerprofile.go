// Package ownerprofile stores the owner's profile/persona document as a single
// Markdown file inside the Obsidian vault. It is the single source of truth for
// the dispatch bot's role, and is read/written by the bot, the MCP server
// (for the Cowork app), and edited directly in Obsidian.
package ownerprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProfileFilename is the fixed name of the profile note inside the vault.
const ProfileFilename = "Profile.md"

// ErrDisabled signals no vault directory is configured.
var ErrDisabled = errors.New("ownerprofile: no vault configured")

// Store reads and writes <vaultDir>/Profile.md. Zero-value / empty-dir Store is
// disabled — every method returns ErrDisabled, so callers can wire it
// unconditionally (mirrors folderread.Root).
type Store struct {
	dir string
	mu  sync.RWMutex
}

// New returns a Store rooted at vaultDir. Empty vaultDir → disabled Store.
func New(vaultDir string) *Store {
	return &Store{dir: strings.TrimSpace(vaultDir)}
}

// Enabled reports whether a vault directory is configured.
func (s *Store) Enabled() bool { return s != nil && s.dir != "" }

func (s *Store) path() string { return filepath.Join(s.dir, ProfileFilename) }

// Read returns the profile contents. A missing file yields ("", nil).
func (s *Store) Read() (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
