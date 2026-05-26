// Package claude wraps the Claude Code CLI for document classification.
// No API key required — uses the existing Claude Code authentication.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultModel is used when none is configured.
const DefaultModel = "claude-haiku-4-5"

// Metadata is the structured output the classifier produces per document.
type Metadata struct {
	DocType  string   `json:"doc_type"`  // policy|promotion|template|other
	Product  string   `json:"product"`   // life|medical|motor|travel|business|""
	Language string   `json:"language"`  // en|zh|ms|mixed
	Audience []string `json:"audience"`  // free-form tags
	Summary  string   `json:"summary"`   // short blurb
	KeyTerms []string `json:"key_terms"` // keywords
}

// Hints are optional deterministic values derived from folder path.
type Hints struct {
	DocType  string
	Product  string
	Language string
}

// Client shells out to the `claude --print` CLI for classification.
type Client struct {
	mu        sync.RWMutex
	model     string
	claudeBin string
	apiCalls  int64
}

// ErrNoAPIKey is kept for interface compatibility — means CLI not found.
var ErrNoAPIKey = errors.New("claude CLI not found in PATH")

// New returns a Client. The apiKey argument is ignored; Claude Code auth is used.
func New(_, model string) *Client {
	bin, _ := exec.LookPath("claude")
	if bin == "" {
		bin = "claude"
	}
	c := &Client{claudeBin: bin}
	c.SetModel(model)
	return c
}

// SetAPIKey is a no-op — auth is handled by Claude Code.
func (c *Client) SetAPIKey(string) {}

// APIKeyPresent reports whether the claude binary is reachable.
func (c *Client) APIKeyPresent() bool { return c.claudeBin != "" }

// SetModel sets the model ID (empty → DefaultModel).
func (c *Client) SetModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if model == "" {
		model = DefaultModel
	}
	c.model = model
}

// Model returns the currently configured model.
func (c *Client) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

// APICalls returns the running count of classification calls this process has made.
func (c *Client) APICalls() int64 { return atomic.LoadInt64(&c.apiCalls) }

// claudeExitError formats a non-zero exit from the `claude` CLI. It surfaces
// both stderr and stdout because `claude --print` frequently writes its real
// error (auth, login, model) to stdout, leaving stderr empty — which otherwise
// produced a useless "claude exited 1: " with no cause.
func claudeExitError(code int, stderr, stdout []byte) error {
	detail := strings.TrimSpace(string(stderr))
	if out := strings.TrimSpace(string(stdout)); out != "" {
		if detail == "" {
			detail = out
		} else {
			detail += " | " + out
		}
	}
	return fmt.Errorf("claude exited %d: %s", code, truncate(detail, 300))
}

// Reply generates a conversational reply. systemPrompt sets the agent role;
// conversation contains the full formatted context (history + incoming message).
// Returns raw reply text — no JSON parsing.
func (c *Client) Reply(ctx context.Context, systemPrompt, conversation string) (string, error) {
	c.mu.RLock()
	model := c.model
	claudeBin := c.claudeBin
	c.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	prompt := systemPrompt + "\n\n" + conversation
	cmd := exec.CommandContext(ctx, claudeBin, "--print", "--model", model)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", claudeExitError(exitErr.ExitCode(), exitErr.Stderr, out)
		}
		return "", err
	}
	atomic.AddInt64(&c.apiCalls, 1)
	return strings.TrimSpace(string(out)), nil
}

// Classify sends extracted text to Claude for metadata extraction.
func (c *Client) Classify(ctx context.Context, text string, h Hints) (Metadata, error) {
	return c.classify(ctx, text, nil, "", h)
}

// ClassifyImage sends image bytes to Claude vision via a temp file.
func (c *Client) ClassifyImage(ctx context.Context, imageBytes []byte, mimeType string, h Hints) (Metadata, error) {
	return c.classify(ctx, "", imageBytes, mimeType, h)
}

func (c *Client) classify(ctx context.Context, text string, imageBytes []byte, mimeType string, h Hints) (Metadata, error) {
	c.mu.RLock()
	model := c.model
	claudeBin := c.claudeBin
	c.mu.RUnlock()

	if len(text) > 60000 {
		text = text[:60000] + "\n[truncated]"
	}

	system := buildSystemPrompt(h)

	args := []string{"--print", "--model", model}

	var prompt string
	var tmpFile string

	if len(imageBytes) > 0 {
		ext := mimeToExt(mimeType)
		f, err := os.CreateTemp("", "claude-classify-*"+ext)
		if err != nil {
			return Metadata{}, fmt.Errorf("temp file: %w", err)
		}
		tmpFile = f.Name()
		defer os.Remove(tmpFile)
		if _, err := f.Write(imageBytes); err != nil {
			f.Close()
			return Metadata{}, fmt.Errorf("write temp image: %w", err)
		}
		f.Close()
		args = append(args, "--allowedTools", "Read", "--add-dir", os.TempDir())
		prompt = system + "\n\nClassify the image document at path: " + tmpFile + "\nRead it and return the JSON object only."
	} else {
		prompt = system + "\n\nClassify the document below. Return the JSON object only.\n\n---\n" + text
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		cmd := exec.CommandContext(ctx, claudeBin, args...)
		cmd.Stdin = strings.NewReader(prompt)
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				lastErr = claudeExitError(exitErr.ExitCode(), exitErr.Stderr, out)
			} else {
				lastErr = err
			}
			continue
		}

		atomic.AddInt64(&c.apiCalls, 1)

		raw := strings.TrimSpace(string(out))
		raw = stripJSONFence(raw)
		raw = extractJSONObject(raw)
		if raw == "" {
			lastErr = errors.New("classifier returned no text")
			continue
		}
		var md Metadata
		if err := json.Unmarshal([]byte(raw), &md); err != nil {
			lastErr = fmt.Errorf("parse classifier JSON: %w (raw: %q)", err, truncate(raw, 200))
			continue
		}
		return applyHints(md, h), nil
	}
	if lastErr == nil {
		lastErr = errors.New("classify failed after retry")
	}
	return Metadata{}, lastErr
}

func buildSystemPrompt(h Hints) string {
	var sb strings.Builder
	sb.WriteString(`You classify insurance-industry documents (policies, promotions, brochures, templates) for a CRM.

Return ONLY a single JSON object with exactly these fields and no prose:
{
  "doc_type":  "policy" | "promotion" | "template" | "other",
  "product":   "life" | "medical" | "motor" | "travel" | "business" | "" ,
  "language":  "en" | "zh" | "ms" | "mixed",
  "audience":  [ short strings describing who this is for, e.g. "new_parent", "business_owner" ],
  "summary":   "one or two sentences, max 300 chars",
  "key_terms": [ 5-15 short keywords or phrases for retrieval ]
}

Rules:
- Output MUST parse as JSON. No markdown fences, no commentary.
- Use empty string "" for product when not applicable.
- Audience is free-form tags; prefer lowercase snake_case.
- If you cannot determine a field, pick the closest allowed value or "".
`)
	if h.DocType != "" || h.Product != "" || h.Language != "" {
		sb.WriteString("\nHints from folder path (prefer these if plausible):\n")
		if h.DocType != "" {
			fmt.Fprintf(&sb, "- doc_type: %s\n", h.DocType)
		}
		if h.Product != "" {
			fmt.Fprintf(&sb, "- product: %s\n", h.Product)
		}
		if h.Language != "" {
			fmt.Fprintf(&sb, "- language: %s\n", h.Language)
		}
	}
	return sb.String()
}

func applyHints(md Metadata, h Hints) Metadata {
	if h.DocType != "" {
		md.DocType = h.DocType
	}
	if h.Product != "" {
		md.Product = h.Product
	}
	if h.Language != "" {
		md.Language = h.Language
	}
	return md
}

func mimeToExt(mimeType string) string {
	switch {
	case strings.Contains(mimeType, "png"):
		return ".png"
	case strings.Contains(mimeType, "gif"):
		return ".gif"
	case strings.Contains(mimeType, "webp"):
		return ".webp"
	default:
		return ".jpg"
	}
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

var jsonObjectRe = regexp.MustCompile(`(?s)\{.*\}`)

func extractJSONObject(s string) string {
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return s
	}
	if m := jsonObjectRe.FindString(s); m != "" {
		return m
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
