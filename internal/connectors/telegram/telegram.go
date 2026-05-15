// Package telegram is a minimal Telegram Bot API client with long-poll updates
// and an owner-allowlist message handler. No third-party SDK — plain JSON
// over HTTP via net/http.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiBase = "https://api.telegram.org"

// User is a Telegram user (subset of fields we use).
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is a Telegram chat (subset).
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// Message is an inbound or outbound message (subset).
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

// Update is one delta from getUpdates.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Handler is called for each inbound message that passes the allowlist.
// Return a non-empty string to send a reply; empty string to send nothing.
type Handler func(ctx context.Context, m *Message) string

// Client is a Bot API client. Zero value is not usable — construct via New.
type Client struct {
	token      string
	ownerIDs   map[int64]bool
	httpClient *http.Client
	logger     *log.Logger
	handler    Handler

	mu     sync.RWMutex
	offset int64
}

// Option configures a Client.
type Option func(*Client)

// WithLogger overrides the default logger.
func WithLogger(l *log.Logger) Option { return func(c *Client) { c.logger = l } }

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// New constructs a Client. token is the Bot API token from @BotFather.
// ownerIDs are the Telegram user IDs allowed to interact; all others are dropped.
func New(token string, ownerIDs []int64, opts ...Option) *Client {
	c := &Client{
		token:    token,
		ownerIDs: make(map[int64]bool, len(ownerIDs)),
		httpClient: &http.Client{
			Timeout: 65 * time.Second, // long-poll = 60s + 5s buffer
		},
		logger: log.Default(),
	}
	for _, id := range ownerIDs {
		c.ownerIDs[id] = true
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SetHandler swaps the inbound-message handler. Safe to call before or after Start.
func (c *Client) SetHandler(h Handler) {
	c.mu.Lock()
	c.handler = h
	c.mu.Unlock()
}

// IsAllowed reports whether the given Telegram user ID is in the owner allowlist.
func (c *Client) IsAllowed(userID int64) bool {
	return c.ownerIDs[userID]
}

// GetMe returns the bot's own identity. Used for "test connection" UI.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var resp struct {
		OK     bool  `json:"ok"`
		Result *User `json:"result"`
	}
	if err := c.call(ctx, "getMe", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK || resp.Result == nil {
		return nil, errors.New("getMe: empty response")
	}
	return resp.Result, nil
}

// SendMessage sends a plain text reply to chatID.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) (*Message, error) {
	body := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	var resp struct {
		OK     bool     `json:"ok"`
		Result *Message `json:"result"`
	}
	if err := c.call(ctx, "sendMessage", body, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New("sendMessage: api returned ok=false")
	}
	return resp.Result, nil
}

// SendPhoto uploads a local file as a photo to chatID with optional caption.
// path must be a readable local file.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, path, caption string) error {
	// For v1, use file URL upload via multipart only if needed; the simpler
	// route — uploading a local file — is multipart/form-data. Caller passes
	// the path; we POST raw bytes. Implementation deferred until P4 (image
	// gen) actually needs it. Stubbed to return a clear error so callers fail
	// loudly rather than silently.
	return fmt.Errorf("SendPhoto: not implemented in P1 (deferred to P4)")
}

// Start begins the long-poll loop. Blocks until ctx is cancelled.
// Errors are logged and the loop continues — a bot should be resilient to
// transient network failures.
func (c *Client) Start(ctx context.Context) error {
	c.logger.Printf("telegram: long-poll starting")
	for {
		if err := ctx.Err(); err != nil {
			c.logger.Printf("telegram: long-poll stopped: %v", err)
			return err
		}

		updates, err := c.getUpdates(ctx, 60)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.logger.Printf("telegram: getUpdates: %v (backing off 5s)", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}

		for _, u := range updates {
			c.mu.Lock()
			if u.UpdateID >= c.offset {
				c.offset = u.UpdateID + 1
			}
			c.mu.Unlock()
			c.dispatch(ctx, u)
		}
	}
}

// dispatch routes one update to the handler, enforcing the allowlist.
func (c *Client) dispatch(ctx context.Context, u Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	if !c.IsAllowed(u.Message.From.ID) {
		c.logger.Printf("telegram: drop msg from non-owner %d (@%s)", u.Message.From.ID, u.Message.From.Username)
		return
	}

	c.mu.RLock()
	h := c.handler
	c.mu.RUnlock()

	if h == nil {
		c.logger.Printf("telegram: no handler set, dropping msg from %d", u.Message.From.ID)
		return
	}

	reply := h(ctx, u.Message)
	if reply == "" {
		return
	}
	if _, err := c.SendMessage(ctx, u.Message.Chat.ID, reply); err != nil {
		c.logger.Printf("telegram: sendMessage failed: %v", err)
	}
}

func (c *Client) getUpdates(ctx context.Context, timeoutSec int) ([]Update, error) {
	c.mu.RLock()
	offset := c.offset
	c.mu.RUnlock()

	params := url.Values{}
	params.Set("timeout", strconv.Itoa(timeoutSec))
	if offset > 0 {
		params.Set("offset", strconv.FormatInt(offset, 10))
	}

	var resp struct {
		OK     bool     `json:"ok"`
		Result []Update `json:"result"`
	}
	if err := c.callForm(ctx, "getUpdates", params, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, errors.New("getUpdates: api ok=false")
	}
	return resp.Result, nil
}

// call POSTs a JSON body to method and decodes into out.
func (c *Client) call(ctx context.Context, method string, body any, out any) error {
	endpoint := fmt.Sprintf("%s/bot%s/%s", apiBase, c.token, method)
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", method, err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s: read body: %w", method, err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s: http %d: %s", method, res.StatusCode, truncate(string(raw), 200))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: decode: %w (body=%s)", method, err, truncate(string(raw), 200))
		}
	}
	return nil
}

// callForm POSTs URL-encoded form params to method and decodes into out.
func (c *Client) callForm(ctx context.Context, method string, form url.Values, out any) error {
	endpoint := fmt.Sprintf("%s/bot%s/%s", apiBase, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("%s: read body: %w", method, err)
	}
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("%s: http %d: %s", method, res.StatusCode, truncate(string(raw), 200))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: decode: %w", method, err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
