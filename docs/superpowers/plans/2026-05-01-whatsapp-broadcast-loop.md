# WhatsApp Broadcast Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move bulk-send loop from Claude Desktop (1 tool call per contact) to Claude Bridge (1 tool call submits the whole job, app loops in background with safe pacing, optional per-contact AI personalization, and live browser progress bar).

**Architecture:** Extend existing `internal/batch/queue.go` (already platform-agnostic) by adding a WhatsApp executor case in `Server.executeBatchJob`. New MCP tool `batch_whatsapp_messages` accepts a list of recipients, an optional template, and a `personalize` flag. When `personalize=true`, the executor calls `claude.Client.Reply()` per contact to generate a tailored message before sending. Browser progress page subscribes via Server-Sent Events. Contact-tier-aware delay computation (Active / Quiet / New) reduces ban risk.

**Tech Stack:**
- Go (existing): `internal/batch`, `internal/server`, `internal/connectors/whatsapp`, `internal/claude`, `internal/mcp`, `internal/store`
- WhatsApp: whatsmeow (existing)
- Personalization: Claude CLI via `claude.Client.Reply()` (existing)
- Frontend: vanilla JS + SSE
- Persistence: SQLite (existing `internal/store`)

**Out of scope for v1 (defer):**
- Crash-recovery (queue is in-memory; restart loses pending jobs — document as known limit)
- Daily-cap hard enforcement (v1 = warn-only counter)
- Group-JID broadcast (v1 = explicit recipient list only)
- WhatsApp Business API path (Track B — separate plan)

---

## File Structure

**Create:**
- `internal/broadcast/personalize.go` — wraps claude.Reply for per-contact text generation
- `internal/broadcast/tier.go` — classify contact into Active / Quiet / New based on `cached_messages`
- `internal/broadcast/template.go` — substitute `{{name}}`, `{{push_name}}` placeholders
- `internal/broadcast/personalize_test.go`
- `internal/broadcast/tier_test.go`
- `internal/broadcast/template_test.go`
- `internal/server/sse.go` — SSE helper for batch progress streaming
- `internal/server/html_broadcast.go` — `/broadcasts/{id}` progress page

**Modify:**
- `internal/batch/queue.go` — add `Notes` map field to `Job` for tier/personalize metadata; add subscriber channel for SSE
- `internal/server/server.go` — add `whatsapp` case to `executeBatchJob` (~line 101); wire personalize/tier; register `/broadcasts/`, `/api/batch/events` routes; add `daily_send_count` warn check
- `internal/mcp/mcp.go` — add `batch_whatsapp_messages` tool definition (after line 359) and executor case (after line 512)
- `internal/connectors/whatsapp/whatsapp.go` — expose helper `GetContact(jid)` returning push name + last-message-time (used by tier classifier)
- `internal/server/html_dashboard.go` — add "Recent Broadcasts" card with link to progress pages

---

## Task 1: Add `Notes` field to batch Job (zero-cost metadata for tier/personalize)

**Files:**
- Modify: `internal/batch/queue.go:36-46`

- [ ] **Step 1: Add Notes field to Job struct**

```go
// Job is a single action in a batch.
type Job struct {
	ID         int               `json:"id"`
	Type       JobType           `json:"type"`
	Platform   string            `json:"platform"`
	Params     map[string]string `json:"params"`
	Notes      map[string]string `json:"notes,omitempty"` // tier, personalized_text, etc.
	Status     JobStatus         `json:"status"`
	Error      string            `json:"error,omitempty"`
	StartedAt  *time.Time        `json:"started_at,omitempty"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
}
```

- [ ] **Step 2: Initialize Notes in Submit loop**

In `Submit` at `internal/batch/queue.go:109-117`, change job creation:

```go
for i, params := range items {
	notes := make(map[string]string)
	// caller may pre-populate by setting notes_<key> in params; strip them out:
	for k, v := range params {
		if strings.HasPrefix(k, "_note_") {
			notes[strings.TrimPrefix(k, "_note_")] = v
			delete(params, k)
		}
	}
	batch.Jobs = append(batch.Jobs, &Job{
		ID:       i + 1,
		Type:     jobType,
		Platform: platform,
		Params:   params,
		Notes:    notes,
		Status:   StatusPending,
	})
}
```

Also add `import "strings"` to the file.

- [ ] **Step 3: Build to confirm compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add internal/batch/queue.go
git commit -m "batch: add Notes map to Job for per-job metadata"
```

---

## Task 2: Add SSE subscriber channel to batch.Queue

**Files:**
- Modify: `internal/batch/queue.go`

- [ ] **Step 1: Add subscriber types**

Add after line 73 (the Queue struct):

```go
// Update is a progress event broadcast to SSE subscribers.
type Update struct {
	BatchID  string    `json:"batch_id"`
	JobID    int       `json:"job_id"`
	Status   JobStatus `json:"status"`
	Progress int       `json:"progress"`
	Total    int       `json:"total"`
	Error    string    `json:"error,omitempty"`
	Note     string    `json:"note,omitempty"` // human-readable, e.g. "personalizing..." / "sent"
}

type subscriber struct {
	ch       chan Update
	batchID  string // empty = all batches
}
```

Add fields to Queue struct:

```go
type Queue struct {
	mu          sync.RWMutex
	batches     map[string]*Batch
	executor    ExecutorFunc
	logger      *log.Logger
	counter     int
	subscribers []*subscriber
	subMu       sync.Mutex
}
```

- [ ] **Step 2: Add Subscribe / Unsubscribe / publish methods**

Append to `internal/batch/queue.go`:

```go
// Subscribe returns a channel that receives Update events. If batchID is non-empty,
// only events for that batch are forwarded. Caller must call Unsubscribe when done.
func (q *Queue) Subscribe(batchID string) (<-chan Update, func()) {
	sub := &subscriber{
		ch:      make(chan Update, 32),
		batchID: batchID,
	}
	q.subMu.Lock()
	q.subscribers = append(q.subscribers, sub)
	q.subMu.Unlock()

	cancel := func() {
		q.subMu.Lock()
		defer q.subMu.Unlock()
		for i, s := range q.subscribers {
			if s == sub {
				q.subscribers = append(q.subscribers[:i], q.subscribers[i+1:]...)
				close(s.ch)
				return
			}
		}
	}
	return sub.ch, cancel
}

func (q *Queue) publish(u Update) {
	q.subMu.Lock()
	defer q.subMu.Unlock()
	for _, s := range q.subscribers {
		if s.batchID != "" && s.batchID != u.BatchID {
			continue
		}
		select {
		case s.ch <- u:
		default: // drop if subscriber lagging
		}
	}
}
```

- [ ] **Step 3: Publish updates from processBatch**

In `processBatch` at `internal/batch/queue.go:172-250`, add `q.publish(...)` calls after each status transition.

After `job.Status = StatusRunning` (line 210):

```go
q.publish(Update{BatchID: batchID, JobID: job.ID, Status: StatusRunning, Progress: batch.Progress, Total: batch.Total, Note: "running"})
```

After `job.Status = StatusFailed` (line 222):

```go
q.publish(Update{BatchID: batchID, JobID: job.ID, Status: StatusFailed, Progress: batch.Progress, Total: batch.Total, Error: err.Error()})
```

After `job.Status = StatusCompleted` and `batch.Progress++` (line 226-227):

```go
q.publish(Update{BatchID: batchID, JobID: job.ID, Status: StatusCompleted, Progress: batch.Progress, Total: batch.Total, Note: "sent"})
```

After batch finished (line 247):

```go
q.publish(Update{BatchID: batchID, Status: batch.Status, Progress: batch.Progress, Total: batch.Total, Note: "batch finished"})
```

- [ ] **Step 4: Build to confirm compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/batch/queue.go
git commit -m "batch: add SSE subscriber channel for live progress streaming"
```

---

## Task 3: Template variable substitution

**Files:**
- Create: `internal/broadcast/template.go`
- Create: `internal/broadcast/template_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/broadcast/template_test.go`:

```go
package broadcast

import "testing"

func TestRender_BasicSubstitution(t *testing.T) {
	out := Render("Hi {{name}}!", map[string]string{"name": "Alice"})
	if out != "Hi Alice!" {
		t.Fatalf("got %q, want %q", out, "Hi Alice!")
	}
}

func TestRender_MissingKeyPreserved(t *testing.T) {
	out := Render("Hi {{name}}, your {{thing}} is ready", map[string]string{"name": "Alice"})
	if out != "Hi Alice, your {{thing}} is ready" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_MultipleSameKey(t *testing.T) {
	out := Render("{{name}} said hi to {{name}}", map[string]string{"name": "Bob"})
	if out != "Bob said hi to Bob" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_EmptyVars(t *testing.T) {
	out := Render("hello world", nil)
	if out != "hello world" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_WhitespaceTolerant(t *testing.T) {
	out := Render("Hi {{ name }}!", map[string]string{"name": "Alice"})
	if out != "Hi Alice!" {
		t.Fatalf("got %q", out)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/broadcast/... -run TestRender -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement template.go**

Create `internal/broadcast/template.go`:

```go
// Package broadcast provides helpers for personalized bulk-send loops on top of internal/batch.
package broadcast

import (
	"regexp"
	"strings"
)

var placeholderRE = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// Render replaces {{key}} and {{ key }} placeholders in template using vars.
// Missing keys are left as-is so the original placeholder is visible in the output.
func Render(template string, vars map[string]string) string {
	if len(vars) == 0 {
		return template
	}
	return placeholderRE.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/broadcast/... -run TestRender -v`
Expected: 5/5 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broadcast/template.go internal/broadcast/template_test.go
git commit -m "broadcast: add template variable substitution"
```

---

## Task 4: Contact tier classifier

**Files:**
- Create: `internal/broadcast/tier.go`
- Create: `internal/broadcast/tier_test.go`

Tier rules (research-backed, from `chatappquestions.com` + `a2c.chat`):
- **Active**: contact replied within last 30 days. Safest. Default delay 30s.
- **Quiet**: saved contact, no reply in 30+ days. Medium risk. Default delay 60s.
- **New**: no chat history. Highest risk. Default delay 120s.

- [ ] **Step 1: Write failing tests**

Create `internal/broadcast/tier_test.go`:

```go
package broadcast

import (
	"testing"
	"time"
)

func TestClassify_NoHistory_IsNew(t *testing.T) {
	got := Classify(ContactStats{HasHistory: false})
	if got != TierNew {
		t.Fatalf("got %s, want %s", got, TierNew)
	}
}

func TestClassify_RecentReply_IsActive(t *testing.T) {
	got := Classify(ContactStats{
		HasHistory:   true,
		LastInbound:  time.Now().Add(-7 * 24 * time.Hour),
	})
	if got != TierActive {
		t.Fatalf("got %s, want %s", got, TierActive)
	}
}

func TestClassify_OldReply_IsQuiet(t *testing.T) {
	got := Classify(ContactStats{
		HasHistory:   true,
		LastInbound:  time.Now().Add(-90 * 24 * time.Hour),
	})
	if got != TierQuiet {
		t.Fatalf("got %s, want %s", got, TierQuiet)
	}
}

func TestClassify_HistoryButNeverReplied_IsQuiet(t *testing.T) {
	got := Classify(ContactStats{
		HasHistory:  true,
		LastInbound: time.Time{}, // zero — never replied
	})
	if got != TierQuiet {
		t.Fatalf("got %s, want %s", got, TierQuiet)
	}
}

func TestDelayFor_PerTierDefaults(t *testing.T) {
	cases := map[Tier][2]int{
		TierActive: {30, 60},
		TierQuiet:  {60, 120},
		TierNew:    {120, 300},
	}
	for tier, want := range cases {
		minD, maxD := DelayFor(tier)
		if minD != want[0] || maxD != want[1] {
			t.Fatalf("tier %s: got [%d,%d], want %v", tier, minD, maxD, want)
		}
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/broadcast/... -run "TestClassify|TestDelayFor" -v`
Expected: FAIL — types undefined.

- [ ] **Step 3: Implement tier.go**

Create `internal/broadcast/tier.go`:

```go
package broadcast

import "time"

// Tier categorizes a contact by ban-risk for bulk sends.
type Tier string

const (
	TierActive Tier = "active" // recent two-way chat — safest
	TierQuiet  Tier = "quiet"  // saved but no recent inbound — medium risk
	TierNew    Tier = "new"    // no chat history — highest risk
)

// ContactStats is the input to Classify, populated from cached_messages.
type ContactStats struct {
	HasHistory  bool      // any cached messages with this JID
	LastInbound time.Time // last time the contact sent a message TO us; zero if never
}

const activeWindow = 30 * 24 * time.Hour

// Classify returns the tier for a contact given its message history.
func Classify(s ContactStats) Tier {
	if !s.HasHistory {
		return TierNew
	}
	if !s.LastInbound.IsZero() && time.Since(s.LastInbound) <= activeWindow {
		return TierActive
	}
	return TierQuiet
}

// DelayFor returns recommended (minSeconds, maxSeconds) delay range for a tier.
// Values are conservative — designed to keep block-rate <2% per 30 days.
func DelayFor(t Tier) (int, int) {
	switch t {
	case TierActive:
		return 30, 60
	case TierQuiet:
		return 60, 120
	case TierNew:
		return 120, 300
	default:
		return 60, 120
	}
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/broadcast/... -run "TestClassify|TestDelayFor" -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broadcast/tier.go internal/broadcast/tier_test.go
git commit -m "broadcast: classify contacts by tier + per-tier delay defaults"
```

---

## Task 5: Personalization helper (calls Claude per contact)

**Files:**
- Create: `internal/broadcast/personalize.go`
- Create: `internal/broadcast/personalize_test.go`

Personalization input: `{name, recent_messages, base_template, instructions}`.
Output: a single string (no JSON parsing).

- [ ] **Step 1: Write failing tests**

Create `internal/broadcast/personalize_test.go`:

```go
package broadcast

import (
	"context"
	"strings"
	"testing"
)

type fakeClient struct {
	lastSystem string
	lastUser   string
	reply      string
	err        error
}

func (f *fakeClient) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastSystem = system
	f.lastUser = user
	return f.reply, f.err
}

func TestPersonalize_BuildsPromptWithContext(t *testing.T) {
	fc := &fakeClient{reply: "Hi Alice, hope your week is great!"}
	p := Personalizer{Claude: fc}
	out, err := p.Generate(context.Background(), Input{
		ContactName:    "Alice",
		BaseTemplate:   "Hi {{name}}, new offer in.",
		Instructions:   "Keep tone warm and informal.",
		RecentMessages: []string{"thanks so much!", "yes please send"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "Hi Alice, hope your week is great!" {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(fc.lastUser, "Alice") {
		t.Fatalf("user prompt missing name: %s", fc.lastUser)
	}
	if !strings.Contains(fc.lastUser, "thanks so much!") {
		t.Fatalf("user prompt missing recent message")
	}
	if !strings.Contains(fc.lastSystem, "warm and informal") {
		t.Fatalf("system prompt missing instructions")
	}
}

func TestPersonalize_FallbackOnError(t *testing.T) {
	fc := &fakeClient{err: context.DeadlineExceeded}
	p := Personalizer{Claude: fc}
	out, err := p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}, offer.",
	})
	if err != nil {
		t.Fatalf("expected fallback, got err: %v", err)
	}
	// Fallback = template-rendered string, not Claude output.
	if out != "Hi Alice, offer." {
		t.Fatalf("got %q, want template-rendered fallback", out)
	}
}

func TestPersonalize_TrimsWhitespace(t *testing.T) {
	fc := &fakeClient{reply: "  \nHi Alice!\n  "}
	p := Personalizer{Claude: fc}
	out, _ := p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}",
	})
	if out != "Hi Alice!" {
		t.Fatalf("got %q", out)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `go test ./internal/broadcast/... -run TestPersonalize -v`
Expected: FAIL — types undefined.

- [ ] **Step 3: Implement personalize.go**

Create `internal/broadcast/personalize.go`:

```go
package broadcast

import (
	"context"
	"fmt"
	"strings"
)

// claudeReplier is the subset of *claude.Client we use; defined as interface for testing.
type claudeReplier interface {
	Reply(ctx context.Context, systemPrompt, conversation string) (string, error)
}

// Input describes one contact's data for personalization.
type Input struct {
	ContactName    string
	BaseTemplate   string   // template with {{name}} etc. — also used as fallback
	Instructions   string   // user-supplied tone/style guidance
	RecentMessages []string // most-recent inbound messages, oldest first
}

// Personalizer wraps claude to generate a single tailored message per contact.
type Personalizer struct {
	Claude claudeReplier
}

const defaultInstructions = "Rephrase the base message naturally for this specific contact. Keep the meaning. One short message. No greetings beyond a name. No emoji unless the original had one. Reply with ONLY the final message text — no preamble, no quotes."

// Generate returns a personalized message. If Claude fails, the rendered template is used as fallback.
func (p Personalizer) Generate(ctx context.Context, in Input) (string, error) {
	rendered := Render(in.BaseTemplate, map[string]string{
		"name":      in.ContactName,
		"push_name": in.ContactName,
	})

	if p.Claude == nil {
		return rendered, nil
	}

	instr := in.Instructions
	if instr == "" {
		instr = defaultInstructions
	}

	system := "You write personalized WhatsApp messages. " + instr
	user := buildUserPrompt(in, rendered)

	out, err := p.Claude.Reply(ctx, system, user)
	if err != nil {
		return rendered, nil // fallback — preserve send rather than fail
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return rendered, nil
	}
	return out, nil
}

func buildUserPrompt(in Input, rendered string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Contact name: %s\n\n", in.ContactName)
	if len(in.RecentMessages) > 0 {
		b.WriteString("Recent messages from this contact (oldest first):\n")
		for _, m := range in.RecentMessages {
			fmt.Fprintf(&b, "  - %s\n", m)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Base message:\n%s\n", rendered)
	return b.String()
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/broadcast/... -run TestPersonalize -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/broadcast/personalize.go internal/broadcast/personalize_test.go
git commit -m "broadcast: per-contact AI personalization with template fallback"
```

---

## Task 6: Helper to populate ContactStats from cached_messages

**Files:**
- Create: `internal/broadcast/stats.go`
- Modify: `internal/store/store.go` — add `LastInboundAt(jid)` query if not present

- [ ] **Step 1: Check existing store**

Run: `grep -n "LastInbound\|last_inbound\|MAX(timestamp)" /Users/gen/Documents/Claude/Projects/Personal\ Assistant\ App/crm-agent/internal/store/store.go`

If a method already exists that returns last inbound message timestamp by JID, skip Step 2.

- [ ] **Step 2: Add LastInboundAt to store**

Add to `internal/store/store.go`:

```go
// LastInboundAt returns the most recent timestamp at which the given JID sent
// us a message (is_outgoing=0). Returns zero time if no inbound messages.
func (s *Store) LastInboundAt(jid string) (time.Time, error) {
	const q = `SELECT MAX(timestamp) FROM cached_messages WHERE sender_id = ? AND is_outgoing = 0`
	var ts sql.NullInt64
	if err := s.db.QueryRow(q, jid).Scan(&ts); err != nil {
		return time.Time{}, err
	}
	if !ts.Valid || ts.Int64 == 0 {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0), nil
}

// HasMessageHistory reports whether any cached_messages exist for the JID
// (inbound or outbound).
func (s *Store) HasMessageHistory(jid string) (bool, error) {
	const q = `SELECT 1 FROM cached_messages WHERE sender_id = ? OR conversation_id = ? LIMIT 1`
	var n int
	err := s.db.QueryRow(q, jid, jid).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
```

(Verify the existing column names by reading the schema definition near the top of `internal/store/store.go` — adjust `sender_id` / `conversation_id` / `timestamp` / `is_outgoing` to match what's there.)

- [ ] **Step 3: Implement stats.go**

Create `internal/broadcast/stats.go`:

```go
package broadcast

import "time"

// statsSource lets us inject mocks in tests; satisfied by *store.Store.
type statsSource interface {
	LastInboundAt(jid string) (time.Time, error)
	HasMessageHistory(jid string) (bool, error)
}

// FetchStats queries the store and returns the ContactStats for a JID.
func FetchStats(src statsSource, jid string) (ContactStats, error) {
	has, err := src.HasMessageHistory(jid)
	if err != nil {
		return ContactStats{}, err
	}
	last, err := src.LastInboundAt(jid)
	if err != nil {
		return ContactStats{}, err
	}
	return ContactStats{HasHistory: has, LastInbound: last}, nil
}
```

- [ ] **Step 4: Build to confirm compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/broadcast/stats.go
git commit -m "broadcast: query helper for contact message history (tier input)"
```

---

## Task 7: Wire WhatsApp executor in server.go

**Files:**
- Modify: `internal/server/server.go:101-114` (executeBatchJob)

- [ ] **Step 1: Add whatsapp case**

Replace the body of `executeBatchJob` with:

```go
func (s *Server) executeBatchJob(ctx context.Context, jobType batch.JobType, platform string, params map[string]string) error {
	switch platform {
	case "facebook":
		switch jobType {
		case batch.JobCreatePost:
			return s.fb.CreatePost(ctx, params["content"], params["page_url"])
		case batch.JobSendMessage:
			return s.fb.Messenger.SendMessage(ctx, params["recipient_id"], params["message"])
		case batch.JobReplyComment:
			return s.fb.Messenger.ReplyToComment(ctx, params["comment_id"], params["message"])
		}
	case "whatsapp":
		switch jobType {
		case batch.JobSendMessage:
			return s.executeWhatsAppSend(ctx, params)
		}
	}
	return fmt.Errorf("unsupported: %s/%s", platform, jobType)
}

// executeWhatsAppSend handles a single WhatsApp send job, optionally personalizing
// the message via Claude before delivering it through the WhatsApp connector.
//
// Required params: phone, message
// Optional params: from_jid, contact_name, personalize ("true"/"false"),
// instructions, recent_msgs (newline-joined)
func (s *Server) executeWhatsAppSend(ctx context.Context, params map[string]string) error {
	phone := params["phone"]
	if phone == "" {
		return fmt.Errorf("missing phone")
	}

	text := params["message"]
	contactName := params["contact_name"]
	if contactName == "" {
		contactName = phone
	}

	// Render template placeholders first (always, even if not personalizing).
	text = broadcast.Render(text, map[string]string{
		"name":      contactName,
		"push_name": contactName,
	})

	if params["personalize"] == "true" && s.agentClient != nil {
		var recent []string
		if rm := params["recent_msgs"]; rm != "" {
			recent = strings.Split(rm, "\n")
		}
		p := broadcast.Personalizer{Claude: s.agentClient}
		generated, err := p.Generate(ctx, broadcast.Input{
			ContactName:    contactName,
			BaseTemplate:   text,
			Instructions:   params["instructions"],
			RecentMessages: recent,
		})
		if err == nil && generated != "" {
			text = generated
		}
	}

	return s.wa.SendMessage(phone, text, params["from_jid"])
}
```

Add imports at top of file if not already present:

```go
import (
	"github.com/<repo>/internal/broadcast"
	"strings"
)
```

(Use the actual module path from `go.mod`.)

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Smoke test**

Manually: post to `/api/batch/submit` with `{"platform":"whatsapp","type":"send_message","items":[{"phone":"<test_jid>","message":"hello {{name}}","contact_name":"World"}],"min_delay_seconds":2,"max_delay_seconds":3}` against a running app with WhatsApp connected to a test account. Verify the test contact receives "hello World".

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go
git commit -m "server: route batch jobs to whatsapp send + personalize hook"
```

---

## Task 8: SSE endpoint for live progress

**Files:**
- Create: `internal/server/sse.go`
- Modify: `internal/server/server.go` — register `/api/batch/events` in `buildMux`

- [ ] **Step 1: Implement SSE handler**

Create `internal/server/sse.go`:

```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleBatchEvents streams batch progress as Server-Sent Events.
// Query param: batch_id (optional — empty = all batches).
func (s *Server) handleBatchEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	batchID := r.URL.Query().Get("batch_id")
	ch, cancel := s.batchQueue.Subscribe(batchID)
	defer cancel()

	// Initial state — flush a snapshot so the client can render before the next event.
	if batchID != "" {
		if b := s.batchQueue.GetBatch(batchID); b != nil {
			snap, _ := json.Marshal(map[string]interface{}{
				"batch_id": b.ID,
				"progress": b.Progress,
				"total":    b.Total,
				"status":   b.Status,
				"snapshot": true,
			})
			fmt.Fprintf(w, "data: %s\n\n", snap)
			flusher.Flush()
		}
	}

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case u, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(u)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 2: Register route**

In `internal/server/server.go` `buildMux`, add (near the other batch routes around line 178-181):

```go
mux.HandleFunc("/api/batch/events", s.handleBatchEvents)
```

- [ ] **Step 3: Build + manual test**

Run: `go build ./...`
Then run the app, open `http://127.0.0.1:10002/api/batch/events?batch_id=foo` in a browser, and submit a batch — events should stream in.

- [ ] **Step 4: Commit**

```bash
git add internal/server/sse.go internal/server/server.go
git commit -m "server: SSE endpoint streaming live batch progress"
```

---

## Task 9: Browser progress page

**Files:**
- Create: `internal/server/html_broadcast.go`
- Modify: `internal/server/server.go` — register `/broadcasts/`

- [ ] **Step 1: Create progress page**

Create `internal/server/html_broadcast.go`:

```go
package server

import (
	"fmt"
	"net/http"
	"strings"
)

func (s *Server) handleBroadcastProgress(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/broadcasts/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, broadcastHTML, id, id)
}

const broadcastHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Broadcast %s — Claude Bridge</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.bar { width: 100%%; height: 24px; background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }
.bar-fill { height: 100%%; background: var(--accent); transition: width 0.4s ease; }
.stat-row { display: flex; gap: 16px; margin: 16px 0; }
.stat { background: var(--bg-card); border: 1px solid var(--border); border-radius: 8px; padding: 12px 16px; flex: 1; text-align: center; }
.stat .v { font-size: 22px; font-weight: 700; }
.stat .l { font-size: 12px; color: var(--text-muted); text-transform: uppercase; }
#log { font-family: monospace; font-size: 12px; max-height: 320px; overflow-y: auto; background: var(--code-bg); padding: 12px; border-radius: 8px; }
.row-fail { color: #c00; }
.row-ok { color: #080; }
button { margin-top: 12px; padding: 8px 16px; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/contacts">Contacts</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle theme"></button>
</nav>
<div class="container narrow">
	<h1>Broadcast Progress</h1>
	<p style="color: var(--text-muted)">Batch ID: <code>%s</code></p>

	<div class="bar"><div class="bar-fill" id="bar" style="width: 0%%"></div></div>
	<div class="stat-row">
		<div class="stat"><div class="v" id="sent">0</div><div class="l">Sent</div></div>
		<div class="stat"><div class="v" id="failed">0</div><div class="l">Failed</div></div>
		<div class="stat"><div class="v" id="total">0</div><div class="l">Total</div></div>
		<div class="stat"><div class="v" id="status">running</div><div class="l">Status</div></div>
	</div>
	<button onclick="cancelBatch()">Cancel</button>
	<h3 style="margin-top:24px">Activity Log</h3>
	<div id="log"></div>
</div>
<script>
const batchId = location.pathname.split('/').pop();
const bar = document.getElementById('bar');
const sentEl = document.getElementById('sent');
const failedEl = document.getElementById('failed');
const totalEl = document.getElementById('total');
const statusEl = document.getElementById('status');
const logEl = document.getElementById('log');
let failed = 0;

const es = new EventSource('/api/batch/events?batch_id=' + encodeURIComponent(batchId));
es.onmessage = (ev) => {
	const u = JSON.parse(ev.data);
	if (u.total) totalEl.textContent = u.total;
	if (u.progress !== undefined) {
		sentEl.textContent = u.progress;
		const pct = u.total ? (100 * u.progress / u.total) : 0;
		bar.style.width = pct.toFixed(1) + '%%';
	}
	if (u.status === 'failed') {
		failed++;
		failedEl.textContent = failed;
	}
	if (u.status && (u.status === 'completed' && !u.job_id)) statusEl.textContent = 'done';
	const row = document.createElement('div');
	row.className = u.status === 'failed' ? 'row-fail' : 'row-ok';
	const t = new Date().toLocaleTimeString();
	row.textContent = '[' + t + '] job ' + (u.job_id || '-') + ' ' + (u.status || '') + (u.error ? ' — ' + u.error : (u.note ? ' — ' + u.note : ''));
	logEl.prepend(row);
};
es.onerror = () => { statusEl.textContent = 'disconnected'; };

async function cancelBatch() {
	if (!confirm('Cancel remaining sends?')) return;
	await fetch('/api/batch/cancel', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({batch_id: batchId})});
	statusEl.textContent = 'cancelled';
}
</script>
</body>
</html>`
```

- [ ] **Step 2: Register route**

In `internal/server/server.go` `buildMux`, near the other page routes around line 131-135:

```go
mux.HandleFunc("/broadcasts/", s.handleBroadcastProgress)
```

- [ ] **Step 3: Build + manual test**

Run: `go build ./...`
Submit a small WhatsApp batch (3 messages) via `/api/batch/submit`, open `http://127.0.0.1:10002/broadcasts/<batch_id>`. The bar should fill as messages send.

- [ ] **Step 4: Commit**

```bash
git add internal/server/html_broadcast.go internal/server/server.go
git commit -m "server: live broadcast progress page with SSE-driven progress bar"
```

---

## Task 10: MCP tool `batch_whatsapp_messages`

**Files:**
- Modify: `internal/mcp/mcp.go` (after line 359 for tool def, after line 512 for executor)

- [ ] **Step 1: Add tool definition**

After the `batch_facebook_messages` block (ends ~line 359), insert:

```go
{
	Name:        "batch_whatsapp_messages",
	Description: "Submit a WhatsApp broadcast as a single batch. The Claude Bridge app loops through recipients in the background with safe per-tier delays (Active 30-60s, Quiet 60-120s, New 120-300s) to avoid Meta's bulk-send detection. When personalize=true, each message is rephrased by Claude per contact using their name and recent messages. Returns batch_id immediately. Track live progress at http://127.0.0.1:10002/broadcasts/{batch_id} or via get_batch_status.",
	InputSchema: inputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"recipients": map[string]interface{}{
				"type":        "array",
				"description": "Array of recipient objects: {phone, contact_name (optional), from_jid (optional)}",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"phone":        map[string]interface{}{"type": "string"},
						"contact_name": map[string]interface{}{"type": "string"},
						"from_jid":     map[string]interface{}{"type": "string"},
					},
					"required": []string{"phone"},
				},
			},
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Base message template. Supports {{name}} and {{push_name}} placeholders.",
			},
			"personalize": map[string]interface{}{
				"type":        "boolean",
				"description": "If true, Claude rephrases the message per contact for naturalness. Default false.",
			},
			"instructions": map[string]interface{}{
				"type":        "string",
				"description": "Tone/style guidance for personalization (e.g. 'warm and informal'). Ignored if personalize=false.",
			},
			"min_delay_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Override min delay between sends. Default = tier-based (recommended). Setting below 15s greatly raises ban risk.",
			},
			"max_delay_seconds": map[string]interface{}{
				"type":        "integer",
				"description": "Override max delay between sends. Default = tier-based.",
			},
		},
		Required: []string{"recipients", "message"},
	},
},
```

Update the description of `get_batch_status` (around line 368) to also mention `batch_whatsapp_messages`:

```go
"description": "The batch ID returned from batch_facebook_posts, batch_facebook_messages, or batch_whatsapp_messages",
```

- [ ] **Step 2: Add executor case**

Find the `case "batch_facebook_messages":` block around line 512 and add after it:

```go
case "batch_whatsapp_messages":
	return m.executeBatchWhatsAppMessages(args)
```

Then add the executor method on `*Server` (in the same file, near the other `executeBatch*` methods):

```go
func (m *Server) executeBatchWhatsAppMessages(args map[string]interface{}) (string, error) {
	recipientsRaw, _ := args["recipients"].([]interface{})
	if len(recipientsRaw) == 0 {
		return "", fmt.Errorf("recipients required")
	}
	template, _ := args["message"].(string)
	if template == "" {
		return "", fmt.Errorf("message required")
	}
	personalize, _ := args["personalize"].(bool)
	instructions, _ := args["instructions"].(string)

	// Per-tier delays unless overridden.
	minD := intArg(args, "min_delay_seconds")
	maxD := intArg(args, "max_delay_seconds")

	items := make([]map[string]string, 0, len(recipientsRaw))
	for _, r := range recipientsRaw {
		rec, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		phone, _ := rec["phone"].(string)
		if phone == "" {
			continue
		}
		name, _ := rec["contact_name"].(string)
		fromJID, _ := rec["from_jid"].(string)

		params := map[string]string{
			"phone":        phone,
			"contact_name": name,
			"from_jid":     fromJID,
			"message":      template,
		}
		if personalize {
			params["personalize"] = "true"
			if instructions != "" {
				params["instructions"] = instructions
			}
		}
		items = append(items, params)
	}

	// Submit via the existing HTTP endpoint so behavior matches the dashboard's
	// /api/batch/submit path. (m.httpClient and m.appURL are existing fields.)
	body := map[string]interface{}{
		"platform":          "whatsapp",
		"type":              "send_message",
		"items":             items,
		"min_delay_seconds": minD,
		"max_delay_seconds": maxD,
	}
	resp, err := m.postJSON("/api/batch/submit", body)
	if err != nil {
		return "", err
	}
	id, _ := resp["batch_id"].(string)
	return fmt.Sprintf("Submitted %d WhatsApp messages. Batch ID: %s\nLive progress: %s/broadcasts/%s",
		len(items), id, m.appURL, id), nil
}

// intArg pulls an int from a JSON args map, returning 0 if absent.
func intArg(args map[string]interface{}, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
```

(If `m.postJSON` / `m.appURL` / `intArg` already exist with different names, use those — read the file around the other executors and match the existing conventions exactly.)

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Manual test from Claude Desktop**

Restart Claude Desktop to pick up the new tool. Ask Claude: "Send a test broadcast to <test_phone> saying 'hi {{name}}, this is a test'." Verify Claude calls `batch_whatsapp_messages`, returns a batch_id, and the test phone receives the message.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/mcp.go
git commit -m "mcp: add batch_whatsapp_messages tool with personalize option"
```

---

## Task 11: Auto-tier delays based on contact history

**Files:**
- Modify: `internal/server/server.go` — `handleBatchSubmit` (~line 895-916)

When the caller doesn't supply explicit min/max delays for a WhatsApp batch, compute per-job delays by tiering each recipient. The current `Submit` API takes one min/max for the whole batch — for v1, we use the **most-restrictive tier** (slowest) as the batch-wide delay, and stash each recipient's tier in `_note_tier` for visibility on the progress page.

- [ ] **Step 1: Update handleBatchSubmit**

Locate the function around line 895 and modify the `whatsapp` path:

```go
func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	// ... existing decode of `body` ...

	if body.Platform == "whatsapp" {
		// Auto-tier each recipient and override delays if caller didn't specify.
		worstTier := broadcast.TierActive
		for _, item := range body.Items {
			phone := item["phone"]
			if phone == "" {
				continue
			}
			stats, err := broadcast.FetchStats(s.store, phone)
			if err != nil {
				continue
			}
			tier := broadcast.Classify(stats)
			item["_note_tier"] = string(tier)
			if tierRank(tier) > tierRank(worstTier) {
				worstTier = tier
			}
		}
		if body.MinDelay == 0 && body.MaxDelay == 0 {
			body.MinDelay, body.MaxDelay = broadcast.DelayFor(worstTier)
		}
	}

	batchID := s.batchQueue.Submit(body.Platform, batch.JobType(body.Type), body.Items, body.MinDelay, body.MaxDelay)
	writeJSON(w, map[string]string{"batch_id": batchID})
}

func tierRank(t broadcast.Tier) int {
	switch t {
	case broadcast.TierActive:
		return 0
	case broadcast.TierQuiet:
		return 1
	case broadcast.TierNew:
		return 2
	}
	return 1
}
```

(Adjust based on how `body` and `body.Items` are actually shaped — read the existing `handleBatchSubmit` first.)

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Manual test**

Submit a batch with mixed recipients (one with chat history, one cold), no min/max delay set. Confirm the resulting batch's delay matches the worst tier and the per-job `notes.tier` reflects each recipient.

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go
git commit -m "server: auto-tier WhatsApp recipients + tier-aware default delays"
```

---

## Task 12: Daily-cap warning counter

**Files:**
- Modify: `internal/server/server.go` — `handleBatchSubmit`
- Modify: `internal/store/store.go` — add `CountSendsLast24h(jid)` (or in-memory counter on Server)

Soft warn (do not block) if today's sends from the active WhatsApp account already exceed 80, and refuse to submit (with a clear error) if exceeding 200. v1 uses an in-memory counter reset at midnight local time.

- [ ] **Step 1: Add counter to Server**

In `internal/server/server.go` Server struct definition, add:

```go
dailySendCount   map[string]int // key = YYYY-MM-DD
dailySendCountMu sync.Mutex
```

Initialize the map in the constructor.

- [ ] **Step 2: Check + increment in handleBatchSubmit**

Before calling `s.batchQueue.Submit` for the WhatsApp path, add:

```go
const softCap = 80
const hardCap = 200

today := time.Now().Format("2006-01-02")
s.dailySendCountMu.Lock()
projected := s.dailySendCount[today] + len(body.Items)
if projected > hardCap {
	s.dailySendCountMu.Unlock()
	http.Error(w, fmt.Sprintf("daily cap exceeded (%d/%d) — wait until tomorrow or reduce batch size", projected, hardCap), http.StatusTooManyRequests)
	return
}
s.dailySendCount[today] = projected
s.dailySendCountMu.Unlock()

resp := map[string]interface{}{}
if projected > softCap {
	resp["warning"] = fmt.Sprintf("approaching daily cap (%d/%d). Sends after this risk Meta detection.", projected, hardCap)
}
batchID := s.batchQueue.Submit(...)
resp["batch_id"] = batchID
writeJSON(w, resp)
```

- [ ] **Step 3: Build + manual test**

Submit a batch of 50, then a batch of 50 — second response should include `warning`. A batch that pushes total above 200 should 429.

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go
git commit -m "server: daily-cap soft warn + hard limit on WhatsApp broadcasts"
```

---

## Task 13: Recent broadcasts card on dashboard

**Files:**
- Modify: `internal/server/html_dashboard.go`

- [ ] **Step 1: Add card markup**

Find the dashboard's section list (search the file for the existing platform cards) and add:

```html
<div class="card">
	<h3>Recent Broadcasts</h3>
	<div id="broadcasts-list"><p style="color:var(--text-muted)">No broadcasts yet.</p></div>
</div>
```

Add JS that loads `/api/batch/list?platform=whatsapp` on dashboard load and renders rows linking to `/broadcasts/{id}`.

```html
<script>
async function loadBroadcasts() {
	try {
		const r = await fetch('/api/batch/list?platform=whatsapp');
		const data = await r.json();
		const list = document.getElementById('broadcasts-list');
		if (!data.batches || !data.batches.length) {
			list.innerHTML = '<p style="color:var(--text-muted)">No broadcasts yet.</p>';
			return;
		}
		list.innerHTML = data.batches.slice(-5).reverse().map(b =>
			'<div style="display:flex;justify-content:space-between;padding:6px 0;border-bottom:1px solid var(--border)">' +
			'<a href="/broadcasts/' + b.id + '">' + b.id + '</a>' +
			'<span style="color:var(--text-muted)">' + b.progress + '/' + b.total + ' — ' + b.status + '</span>' +
			'</div>').join('');
	} catch (e) {}
}
document.addEventListener('DOMContentLoaded', loadBroadcasts);
setInterval(loadBroadcasts, 10000);
</script>
```

- [ ] **Step 2: Manual test**

Reload dashboard. Submit a broadcast. Card refreshes and shows the new batch with a link.

- [ ] **Step 3: Commit**

```bash
git add internal/server/html_dashboard.go
git commit -m "dashboard: recent broadcasts card with progress page links"
```

---

## Task 14: Documentation

**Files:**
- Create: `docs/BROADCAST.md`

- [ ] **Step 1: Write docs**

Document the MCP tool, safe-delay defaults, the personalize flag, the `{{name}}` template, the daily cap, and the progress page URL pattern. Include the Active/Quiet/New tier table from research, and a clear warning that ANY use of personal-account WhatsApp for bulk send is at the user's own risk and that the official WhatsApp Business API path (Track B) is the only fully safe option.

- [ ] **Step 2: Commit**

```bash
git add docs/BROADCAST.md
git commit -m "docs: WhatsApp broadcast tool guide + safe-pacing notes"
```

---

## Self-Review Checklist

Run through these before declaring the plan finished:

- [ ] Every task lists exact files + line numbers where applicable
- [ ] No "TBD" or "implement later" placeholders
- [ ] Type names consistent across tasks: `Tier`, `ContactStats`, `Input`, `Personalizer`, `Render`, `Update`
- [ ] Each task ends with a commit
- [ ] TDD flow only used where unit tests are meaningful (Tasks 3, 4, 5); plumbing tasks use build + manual smoke
- [ ] Safe-delay defaults match the research (Active 30-60s, Quiet 60-120s, New 120-300s)
- [ ] No ban-prevention claim made beyond "reduces detection risk" — no guarantees

---

## Known Limitations (to communicate when shipping)

1. **No crash recovery** — if the app restarts mid-broadcast, pending jobs are lost. Track B (or a follow-up plan) to add a `broadcast_jobs` SQLite table.
2. **In-memory daily counter** — restart resets the counter. Persist in a follow-up.
3. **Dad's preferred 15s interval is below safe range.** UI/MCP description warns; the system does not block at 15s, only at <1s.
4. **Personal-account whatsmeow path is unofficial.** Only the official WhatsApp Business API (Track B) is sanctioned by Meta for bulk sends.
