package knowledge

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/store"

	"github.com/fsnotify/fsnotify"
)

// Watcher walks a folder at startup (feeds the pipeline) and then watches for
// subsequent changes via fsnotify. Call SetFolder to swap the root path at
// runtime.
type Watcher struct {
	store    *store.Store
	pipeline *Pipeline

	mu        sync.Mutex
	folder    string
	fsWatcher *fsnotify.Watcher
	cancel    context.CancelFunc
	running   bool
}

// NewWatcher returns a stopped watcher.
func NewWatcher(s *store.Store, p *Pipeline) *Watcher {
	return &Watcher{store: s, pipeline: p}
}

// SetFolder swaps the watched folder. Empty string stops the watcher.
func (w *Watcher) SetFolder(folder string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// stop prior
	if w.fsWatcher != nil {
		_ = w.fsWatcher.Close()
		w.fsWatcher = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.running = false
	w.folder = folder

	if folder == "" {
		return nil
	}

	info, err := os.Stat(folder)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &fs.PathError{Op: "watch", Path: folder, Err: fs.ErrInvalid}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	// Add the root and every subdirectory.
	if err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			_ = watcher.Add(path)
		}
		return nil
	}); err != nil {
		_ = watcher.Close()
		return err
	}

	w.fsWatcher = watcher
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.running = true

	go w.runEvents(ctx, watcher)
	// No auto-scan: indexing is triggered manually via Rescan().
	log.Printf("[knowledge] watching folder: %s", folder)
	return nil
}

// Folder returns the currently watched folder (empty if none).
func (w *Watcher) Folder() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.folder
}

// Running reports whether the watcher is active.
func (w *Watcher) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// Rescan triggers a full walk of the current folder, queuing every supported
// file. Useful after a config change or manual dashboard button.
func (w *Watcher) Rescan() {
	w.mu.Lock()
	folder := w.folder
	w.mu.Unlock()
	if folder == "" {
		return
	}
	go w.initialScan(folder)
}

// Stop tears down the watcher.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fsWatcher != nil {
		_ = w.fsWatcher.Close()
		w.fsWatcher = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.running = false
}

func (w *Watcher) initialScan(folder string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Skip files already classified so we don't read every file on disk at startup.
	ready, _ := w.store.ReadyDocumentPaths(ctx)

	seen := map[string]bool{}
	_ = filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !IsSupported(path) {
			return nil
		}
		seen[path] = true
		if !ready[path] {
			w.pipeline.Enqueue(path)
		}
		return nil
	})

	// Prune rows that no longer exist on disk AND were inside the watched root.
	paths, err := w.store.AllDocumentPaths(ctx)
	if err == nil {
		for _, p := range paths {
			if !strings.HasPrefix(p, folder) {
				continue
			}
			if !seen[p] {
				w.pipeline.EnqueueDelete(p)
			}
		}
	}
}

func (w *Watcher) runEvents(ctx context.Context, fw *fsnotify.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			if ev.Op&fsnotify.Create != 0 {
				// Watch newly created subdirectories so deletes inside them are tracked.
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = fw.Add(ev.Name)
				}
				// No auto-enqueue: files are indexed only on explicit user request.
			}
			if ev.Op&fsnotify.Remove != 0 {
				w.pipeline.EnqueueDelete(ev.Name)
			}
			if ev.Op&fsnotify.Rename != 0 {
				// The new name arrives as a Create; the old name comes through here.
				w.pipeline.EnqueueDelete(ev.Name)
			}
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			log.Printf("[knowledge] fsnotify error: %v", err)
		}
	}
}
