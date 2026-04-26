package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/claude"
	"claude-bridge/internal/store"
)

// Pipeline orchestrates file lifecycle:
//
//	event → hash+upsert → extract → hint → classify → write
//
// A single worker goroutine serializes API calls. Submit ingestJob values via
// the public Enqueue/EnqueueDelete methods; the watcher and HTTP handlers both
// feed it.
type Pipeline struct {
	store  *store.Store
	client *claude.Client

	in     chan job
	stop   chan struct{}
	stopMu sync.Mutex
	done   chan struct{}
}

type jobKind int

const (
	jobIngest jobKind = iota
	jobDelete
)

type job struct {
	kind jobKind
	path string
}

// NewPipeline wires a pipeline. Call Start() to begin processing.
func NewPipeline(s *store.Store, c *claude.Client) *Pipeline {
	return &Pipeline{
		store:  s,
		client: c,
		in:     make(chan job, 256),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start launches the worker goroutine.
func (p *Pipeline) Start() {
	go p.run()
}

// Stop signals the worker to finish and waits until it exits.
func (p *Pipeline) Stop() {
	p.stopMu.Lock()
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	p.stopMu.Unlock()
	<-p.done
}

// Enqueue schedules ingest (or re-ingest) of a file path.
func (p *Pipeline) Enqueue(path string) {
	select {
	case p.in <- job{kind: jobIngest, path: path}:
	case <-p.stop:
	}
}

// EnqueueDelete schedules deletion of the row for path.
func (p *Pipeline) EnqueueDelete(path string) {
	select {
	case p.in <- job{kind: jobDelete, path: path}:
	case <-p.stop:
	}
}

func (p *Pipeline) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case j := <-p.in:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			switch j.kind {
			case jobIngest:
				if err := p.ingest(ctx, j.path); err != nil {
					log.Printf("[knowledge] ingest %s: %v", j.path, err)
				}
			case jobDelete:
				if err := p.store.DeleteDocumentByPath(ctx, j.path); err != nil {
					log.Printf("[knowledge] delete %s: %v", j.path, err)
				}
			}
			cancel()
		}
	}
}

// IngestOnce runs the full pipeline for one path synchronously. Exposed for
// manual re-ingest triggered from the dashboard.
func (p *Pipeline) IngestOnce(ctx context.Context, path string) error {
	return p.ingest(ctx, path)
}

func (p *Pipeline) ingest(ctx context.Context, path string) error {
	if !IsSupported(path) {
		return nil // silently skip unsupported files
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return p.store.DeleteDocumentByPath(ctx, path)
		}
		return err
	}
	if info.IsDir() {
		return nil
	}

	hash, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}

	existing, err := p.store.GetDocumentByPath(ctx, path)
	if err != nil {
		return err
	}
	if existing != nil && existing.FileHash == hash && existing.Status == "ready" {
		return nil // unchanged, already ingested
	}

	// Rename detection: same hash, different path, and prior row exists.
	if existing == nil {
		if prior, err := p.store.GetDocumentByHash(ctx, hash); err == nil && prior != nil {
			// Only treat as rename if the old path no longer exists.
			if _, err := os.Stat(prior.Path); os.IsNotExist(err) {
				if err := p.store.RenameDocument(ctx, prior.ID, path, filepath.Base(path)); err != nil {
					return err
				}
				return nil
			}
		}
	}

	// Upsert as pending.
	doc := &store.Document{
		Path:      path,
		Filename:  filepath.Base(path),
		FileHash:  hash,
		SizeBytes: info.Size(),
		MimeType:  MimeTypeFor(path),
		Status:    "pending",
	}
	id, err := p.store.UpsertDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}

	// Extract.
	if err := p.store.SetDocumentStatus(ctx, id, "extracting", ""); err != nil {
		return err
	}
	ex, err := Extract(path)
	if err != nil {
		_ = p.store.SetDocumentStatus(ctx, id, "failed", "extract: "+err.Error())
		return err
	}

	// Folder hints.
	hints := HintsFromPath(path)

	// Classify.
	if err := p.store.SetDocumentStatus(ctx, id, "classifying", ""); err != nil {
		return err
	}
	var md claude.Metadata
	if len(ex.ImageBytes) > 0 {
		md, err = p.client.ClassifyImage(ctx, ex.ImageBytes, ex.MimeType, hints)
	} else {
		md, err = p.client.Classify(ctx, ex.Text, hints)
	}
	if err != nil {
		_ = p.store.SetDocumentStatus(ctx, id, "failed", "classify: "+err.Error())
		return err
	}

	// Serialize metadata into the document shape.
	audienceJSON, _ := json.Marshal(md.Audience)
	keyTermsJSON, _ := json.Marshal(md.KeyTerms)
	rawText := ex.Text
	if rawText == "" && len(md.KeyTerms) > 0 {
		rawText = strings.Join(md.KeyTerms, " ")
	}
	dbDoc := &store.Document{
		DocType:  md.DocType,
		Product:  md.Product,
		Language: md.Language,
		Audience: string(audienceJSON),
		Summary:  md.Summary,
		KeyTerms: string(keyTermsJSON),
		RawText:  rawText,
	}
	if err := p.store.UpdateDocumentMetadata(ctx, id, dbDoc); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}
	return nil
}

// hashFile returns sha256 hex of the file contents.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
