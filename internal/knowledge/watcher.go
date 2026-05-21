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
// subsequent changes via fsnotify. Call SetFolder to swap the primary root at
// runtime, or SetExtraRoots to register additional roots (e.g. the Cowork
// output folder) that should be indexed alongside it.
type Watcher struct {
	store    *store.Store
	pipeline *Pipeline

	mu         sync.Mutex
	folder     string   // primary root, set from the dashboard knowledge config
	extraRoots []string // additional roots (e.g. <vault>/Cowork) — persisted across SetFolder
	fsWatcher  *fsnotify.Watcher
	cancel     context.CancelFunc
	running    bool
}

// NewWatcher returns a stopped watcher.
func NewWatcher(s *store.Store, p *Pipeline) *Watcher {
	return &Watcher{store: s, pipeline: p}
}

// SetFolder swaps the primary watched folder. Empty string clears it (extra
// roots, if any, keep being watched). Extra roots are preserved.
func (w *Watcher) SetFolder(folder string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.folder = folder
	return w.rebuildLocked()
}

// SetExtraRoots registers additional directories indexed alongside the primary
// folder (e.g. <vault>/Cowork). Persisted across SetFolder calls. Missing or
// non-directory roots are skipped with a log line rather than failing — the
// Cowork folder may not exist until the first routine writes to it. Pass no
// args to clear.
func (w *Watcher) SetExtraRoots(dirs ...string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extraRoots = w.extraRoots[:0]
	for _, d := range dirs {
		if strings.TrimSpace(d) != "" {
			w.extraRoots = append(w.extraRoots, d)
		}
	}
	return w.rebuildLocked()
}

// allRootsLocked returns the primary folder (if set) followed by extra roots.
// Caller must hold w.mu.
func (w *Watcher) allRootsLocked() []string {
	var roots []string
	if w.folder != "" {
		roots = append(roots, w.folder)
	}
	roots = append(roots, w.extraRoots...)
	return roots
}

// rebuildLocked tears down any running watcher and starts a fresh one covering
// every valid root. The primary folder is validated strictly (a bad path is a
// hard error so the dashboard surfaces it); extra roots are best-effort.
// Caller must hold w.mu.
func (w *Watcher) rebuildLocked() error {
	if w.fsWatcher != nil {
		_ = w.fsWatcher.Close()
		w.fsWatcher = nil
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.running = false

	var roots []string
	if w.folder != "" {
		info, err := os.Stat(w.folder)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &fs.PathError{Op: "watch", Path: w.folder, Err: fs.ErrInvalid}
		}
		roots = append(roots, w.folder)
	}
	for _, r := range w.extraRoots {
		info, err := os.Stat(r)
		if err != nil || !info.IsDir() {
			log.Printf("[knowledge] skip extra root %q: %v", r, err)
			continue
		}
		roots = append(roots, r)
	}
	if len(roots) == 0 {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil // skip unreadable entries
			}
			if d.IsDir() {
				_ = watcher.Add(path)
			}
			return nil
		})
	}

	w.fsWatcher = watcher
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.running = true

	go w.runEvents(ctx, watcher)
	// No auto-scan: indexing is triggered manually via Rescan().
	log.Printf("[knowledge] watching %d root(s): %v", len(roots), roots)
	return nil
}

// Folder returns the primary watched folder (empty if none).
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

// Rescan triggers a full walk of every watched root, queuing every supported
// file. Useful after a config change or manual dashboard button.
func (w *Watcher) Rescan() {
	w.mu.Lock()
	roots := w.allRootsLocked()
	w.mu.Unlock()
	if len(roots) == 0 {
		return
	}
	go w.initialScan(roots)
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

func (w *Watcher) initialScan(roots []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Skip files already classified so we don't read every file on disk at startup.
	ready, _ := w.store.ReadyDocumentPaths(ctx)

	seen := map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	}

	// Prune rows that no longer exist on disk AND were inside one of the
	// watched roots.
	paths, err := w.store.AllDocumentPaths(ctx)
	if err == nil {
		for _, p := range paths {
			if !underAnyRoot(p, roots) {
				continue
			}
			if !seen[p] {
				w.pipeline.EnqueueDelete(p)
			}
		}
	}
}

// underAnyRoot reports whether path is inside any of the roots.
func underAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if strings.HasPrefix(path, root) {
			return true
		}
	}
	return false
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
