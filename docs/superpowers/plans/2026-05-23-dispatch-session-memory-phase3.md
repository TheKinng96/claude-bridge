# Dispatch Session Memory — Phase 3 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Give the dispatch agent session-based memory: messages ≤1h apart share a session with "infinite" recall (rolling summary + raw tail); a background cron compacts idle sessions; and a model-driven `recall_memory` action pulls in relevant past sessions.

**Architecture:** A new `dispatch_sessions` table holds one rolling summary per (channel, owner) conversation; `dispatch_log` gains a `session_id`. On each message the dispatcher resolves the active session (idle gap >1h starts a new one) and builds its prompt from the rolling summary plus the raw turns since the summary. A standalone FTS5 table over summaries powers `recall_memory`. A `Compactor` goroutine ticks periodically and, for sessions idle ≥15min with uncompacted turns, folds the new turns into the summary via a cheap haiku call.

**Tech Stack:** Go 1.25, module `claude-bridge`, SQLite + FTS5. Spec: `docs/superpowers/specs/2026-05-23-telegram-folder-access-design.md` (§6, §7). Phases 1 (folder tools+sonnet) and 2 (multi-step loop) are merged.

**Interface boundary:** The `agent` package never imports `store`. It defines `DispatchStore` / `CompactorStore` interfaces; `*store.Store` implements concrete methods; `main.dispatchStoreAdapter` bridges store rows to the small agent-side types. This plan extends all three.

---

### Task 1: Schema — `dispatch_sessions`, FTS, `dispatch_log.session_id`

**Files:**
- Modify: `internal/store/store.go` (migrations block ~line 418-431; add types near `DispatchLogEntry` ~line 1835)
- Create: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/store/store_test.go
package store

import (
	"context"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSchemaSessionsAndColumn(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// dispatch_sessions table exists (insert + select round-trips).
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO dispatch_sessions (channel, owner_id) VALUES ('telegram','1')`); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	// dispatch_log has session_id column.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO dispatch_log (session_id, channel, owner_id, message, action, user_reply) VALUES (1,'telegram','1','hi','reply','hey')`); err != nil {
		t.Fatalf("insert log with session_id: %v", err)
	}
	// FTS table exists.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO dispatch_sessions_fts (session_id, channel, owner_id, summary) VALUES ('1','telegram','1','test summary')`); err != nil {
		t.Fatalf("insert fts: %v", err)
	}
}
```

Confirm `Store` has a `Close()` method; if not, replace the cleanup with `s.db.Close()`. (`Store.db` is the `*sql.DB` field used throughout store.go.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestSchemaSessionsAndColumn`
Expected: FAIL — `dispatch_sessions` / `session_id` / `dispatch_sessions_fts` do not exist.

- [ ] **Step 3: Add the schema**

Append three statements to the `migrations` slice in `store.go` (right after the `idx_dispatch_log_created` entry, before the closing `}` of the slice):

```go
	`CREATE TABLE IF NOT EXISTS dispatch_sessions (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		channel     TEXT NOT NULL,
		owner_id    TEXT NOT NULL,
		started_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		summary     TEXT NOT NULL DEFAULT '',
		summary_through_log_id INTEGER NOT NULL DEFAULT 0,
		summary_at  DATETIME
	)`,
	`CREATE INDEX IF NOT EXISTS idx_dispatch_sessions_owner ON dispatch_sessions(channel, owner_id, last_at DESC)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS dispatch_sessions_fts USING fts5(
		session_id UNINDEXED, channel UNINDEXED, owner_id UNINDEXED, summary
	)`,
```

Immediately after the `for _, m := range migrations { ... }` loop (after its closing brace, before `return nil`), add a guarded column-add (idempotent across restarts — `CREATE TABLE IF NOT EXISTS` won't alter an existing `dispatch_log`):

```go
	// dispatch_log predates sessions; add session_id if missing. SQLite has no
	// "ADD COLUMN IF NOT EXISTS", so ignore the duplicate-column error on reboot.
	if _, err := s.db.Exec(`ALTER TABLE dispatch_log ADD COLUMN session_id INTEGER NOT NULL DEFAULT 0`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add dispatch_log.session_id: %w", err)
	}
```

Confirm `strings` is imported in store.go (it is). Add the session row types near `DispatchLogEntry`:

```go
// DispatchSessionRow is one row of dispatch_sessions.
type DispatchSessionRow struct {
	ID                  int64
	Channel             string
	OwnerID             string
	Summary             string
	SummaryThroughLogID int64
	StartedAt           time.Time
	LastAt              time.Time
}

// SessionSummaryHit is a recall search match over session summaries.
type SessionSummaryHit struct {
	SessionID int64
	Summary   string
	StartedAt time.Time
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/store/ -run TestSchemaSessionsAndColumn`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): dispatch_sessions table, FTS, and dispatch_log.session_id"
```

---

### Task 2: Store session methods

**Files:**
- Modify: `internal/store/store.go` (new methods; change `SaveDispatchLog` signature)
- Modify: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// append to internal/store/store_test.go
import (
	"context"
	"testing"
	"time"
)

func TestResolveSessionNewThenContinue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Fatal("expected a session id")
	}
	// Within the gap → same session.
	b, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	if b.ID != a.ID {
		t.Fatalf("want same session %d, got %d", a.ID, b.ID)
	}
	// Different owner → different session.
	c, _ := s.ResolveSession(ctx, "telegram", "2", time.Hour)
	if c.ID == a.ID {
		t.Fatal("different owner must get a different session")
	}
}

func TestResolveSessionGapStartsNew(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	// Force last_at far in the past so the gap is exceeded.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE dispatch_sessions SET last_at = datetime('now','-2 hours') WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	b, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	if b.ID == a.ID {
		t.Fatal("gap exceeded must start a new session")
	}
}

func TestSessionTailAfterLogID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	for i := 0; i < 3; i++ {
		if err := s.SaveDispatchLog(ctx, sess.ID, "telegram", "1", "msg", "reply", "ans", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	tail, err := s.SessionTail(ctx, sess.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 3 {
		t.Fatalf("want 3 turns, got %d", len(tail))
	}
	// Oldest-first ordering and ascending IDs.
	if tail[0].ID >= tail[2].ID {
		t.Fatal("tail must be oldest-first")
	}
	// sinceLogID excludes earlier turns.
	tail2, _ := s.SessionTail(ctx, sess.ID, tail[0].ID, 10)
	if len(tail2) != 2 {
		t.Fatalf("want 2 turns after first, got %d", len(tail2))
	}
}

func TestUpdateSummaryAndRecall(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	if err := s.UpdateSessionSummary(ctx, sess.ID, "Discussed Tan policy renewal in March.", 5); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchSessionSummaries(ctx, "telegram", "1", "tan policy", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != sess.ID {
		t.Fatalf("recall failed: %+v", hits)
	}
	// Re-summarizing replaces (no duplicate FTS rows).
	_ = s.UpdateSessionSummary(ctx, sess.ID, "Tan policy renewal confirmed.", 8)
	hits2, _ := s.SearchSessionSummaries(ctx, "telegram", "1", "tan policy", 5)
	if len(hits2) != 1 {
		t.Fatalf("want 1 hit after re-summarize, got %d", len(hits2))
	}
	// Recall is scoped to owner.
	hits3, _ := s.SearchSessionSummaries(ctx, "telegram", "2", "tan policy", 5)
	if len(hits3) != 0 {
		t.Fatalf("recall must be owner-scoped, got %d", len(hits3))
	}
}

func TestIdleSessionsToCompact(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess, _ := s.ResolveSession(ctx, "telegram", "1", time.Hour)
	_ = s.SaveDispatchLog(ctx, sess.ID, "telegram", "1", "m", "reply", "a", "", 0)
	// Active session (last_at = now) → NOT compactable.
	idle, _ := s.IdleSessionsToCompact(ctx, 15*time.Minute, 10)
	if len(idle) != 0 {
		t.Fatalf("active session should not compact, got %d", len(idle))
	}
	// Make it idle.
	_, _ = s.db.ExecContext(ctx, `UPDATE dispatch_sessions SET last_at = datetime('now','-30 minutes') WHERE id=?`, sess.ID)
	idle2, _ := s.IdleSessionsToCompact(ctx, 15*time.Minute, 10)
	if len(idle2) != 1 || idle2[0].ID != sess.ID {
		t.Fatalf("idle session with uncompacted turns should compact, got %+v", idle2)
	}
	// After compaction covers all turns → not compactable again.
	tail, _ := s.SessionTail(ctx, sess.ID, 0, 10)
	_ = s.UpdateSessionSummary(ctx, sess.ID, "done", tail[len(tail)-1].ID)
	idle3, _ := s.IdleSessionsToCompact(ctx, 15*time.Minute, 10)
	if len(idle3) != 0 {
		t.Fatalf("fully-compacted session should not recompact, got %d", len(idle3))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL — `ResolveSession`/`SessionTail`/`UpdateSessionSummary`/`SearchSessionSummaries`/`IdleSessionsToCompact` undefined, and `SaveDispatchLog` signature mismatch (test passes a `session_id` first arg).

- [ ] **Step 3: Implement the methods**

First, change `SaveDispatchLog` to record `session_id` (add `sessionID int64` as the first data arg):

```go
// SaveDispatchLog appends one dispatch turn to the audit log, tagged with its session.
func (s *Store) SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO dispatch_log (session_id, channel, owner_id, message, action, user_reply, error_text, duration_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, channel, ownerID, message, action, userReply, errText, durationMS)
	return err
}
```

Add the new methods (anywhere after `SaveDispatchLog`):

```go
// ResolveSession returns the active session for (channel, owner) when its
// last_at is within gap; otherwise it starts a new session. On a continuing
// session it bumps last_at to now (so idle detection reflects real activity).
func (s *Store) ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (DispatchSessionRow, error) {
	cutoff := time.Now().Add(-gap)
	var r DispatchSessionRow
	err := s.db.QueryRowContext(ctx,
		`SELECT id, summary, summary_through_log_id FROM dispatch_sessions
		 WHERE channel=? AND owner_id=? AND last_at >= ?
		 ORDER BY last_at DESC LIMIT 1`,
		channel, ownerID, cutoff).Scan(&r.ID, &r.Summary, &r.SummaryThroughLogID)
	if errors.Is(err, sql.ErrNoRows) {
		res, e := s.db.ExecContext(ctx,
			`INSERT INTO dispatch_sessions (channel, owner_id) VALUES (?, ?)`, channel, ownerID)
		if e != nil {
			return DispatchSessionRow{}, e
		}
		id, _ := res.LastInsertId()
		return DispatchSessionRow{ID: id, Channel: channel, OwnerID: ownerID}, nil
	}
	if err != nil {
		return DispatchSessionRow{}, err
	}
	r.Channel, r.OwnerID = channel, ownerID
	_, _ = s.db.ExecContext(ctx, `UPDATE dispatch_sessions SET last_at=CURRENT_TIMESTAMP WHERE id=?`, r.ID)
	return r, nil
}

// SessionTail returns the session's turns with id > sinceLogID, oldest first.
func (s *Store) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, channel, owner_id, message, action, user_reply, error_text, duration_ms, created_at
		 FROM dispatch_log
		 WHERE session_id = ? AND id > ?
		 ORDER BY id ASC
		 LIMIT ?`, sessionID, sinceLogID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DispatchLogEntry
	for rows.Next() {
		var e DispatchLogEntry
		if err := rows.Scan(&e.ID, &e.Channel, &e.OwnerID, &e.Message, &e.Action, &e.UserReply, &e.ErrorText, &e.DurationMS, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateSessionSummary stores a refreshed rolling summary, advances the covered
// log id, and refreshes the FTS row (delete + insert — no triggers).
func (s *Store) UpdateSessionSummary(ctx context.Context, sessionID int64, summary string, throughLogID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var channel, ownerID string
	if err := tx.QueryRowContext(ctx,
		`SELECT channel, owner_id FROM dispatch_sessions WHERE id=?`, sessionID).Scan(&channel, &ownerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE dispatch_sessions SET summary=?, summary_through_log_id=?, summary_at=CURRENT_TIMESTAMP WHERE id=?`,
		summary, throughLogID, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM dispatch_sessions_fts WHERE session_id=?`, fmt.Sprint(sessionID)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO dispatch_sessions_fts (session_id, channel, owner_id, summary) VALUES (?,?,?,?)`,
		fmt.Sprint(sessionID), channel, ownerID, summary); err != nil {
		return err
	}
	return tx.Commit()
}

// SearchSessionSummaries finds this owner's past sessions whose summary matches
// query (FTS5), best match first. Returns nothing on an empty/garbage query.
func (s *Store) SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]SessionSummaryHit, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.session_id, f.summary, s.started_at
		 FROM dispatch_sessions_fts f
		 JOIN dispatch_sessions s ON s.id = CAST(f.session_id AS INTEGER)
		 WHERE f.summary MATCH ? AND f.channel = ? AND f.owner_id = ?
		 ORDER BY bm25(dispatch_sessions_fts) ASC
		 LIMIT ?`, match, channel, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummaryHit
	for rows.Next() {
		var h SessionSummaryHit
		var sid string
		if err := rows.Scan(&sid, &h.Summary, &h.StartedAt); err != nil {
			return nil, err
		}
		fmt.Sscan(sid, &h.SessionID)
		out = append(out, h)
	}
	return out, rows.Err()
}

// IdleSessionsToCompact returns sessions idle for at least idleThreshold that
// still have turns past their summary — i.e. safe to compact (owner offline).
func (s *Store) IdleSessionsToCompact(ctx context.Context, idleThreshold time.Duration, limit int) ([]DispatchSessionRow, error) {
	if limit <= 0 {
		limit = 20
	}
	cutoff := time.Now().Add(-idleThreshold)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, channel, owner_id, summary, summary_through_log_id
		 FROM dispatch_sessions s
		 WHERE s.last_at <= ?
		   AND EXISTS (SELECT 1 FROM dispatch_log l WHERE l.session_id = s.id AND l.id > s.summary_through_log_id)
		 ORDER BY s.last_at ASC
		 LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DispatchSessionRow
	for rows.Next() {
		var r DispatchSessionRow
		if err := rows.Scan(&r.ID, &r.Channel, &r.OwnerID, &r.Summary, &r.SummaryThroughLogID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ftsQuery turns free text into a safe FTS5 MATCH expression: alphanumeric
// tokens, each double-quoted, OR-joined. Returns "" when nothing usable remains.
func ftsQuery(q string) string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	var toks []string
	for _, f := range fields {
		if len(f) >= 2 {
			toks = append(toks, `"`+f+`"`)
		}
	}
	return strings.Join(toks, " OR ")
}
```

Confirm `errors`, `database/sql` (`sql.ErrNoRows`), `fmt`, `strings`, `time` are imported in store.go (they are).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/`
Expected: PASS (all session tests).
Then: `go build ./internal/store/` clean. Note: `go build ./...` will FAIL now (callers of the old `SaveDispatchLog` signature in `main.go`/`dispatch.go` break) — that is expected; Tasks 3 and 6 update the callers. Do NOT fix callers in this task.

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat(store): session resolve/tail/summary/recall/idle methods"
```

---

### Task 3: Session-aware dispatcher memory

**Files:**
- Modify: `internal/agent/dispatch.go` (DispatchStore interface ~line 171; DispatchTurn ~line 177; recentTurns/buildDispatchUserPrompt ~line 868-897; Run's session resolve + logAsync ~line 901; add sessionGap const)
- Modify: `internal/agent/dispatch_test.go` (extend `fakeStore`)

- [ ] **Step 1: Write the failing tests**

```go
// append to internal/agent/dispatch_test.go
func TestRunResolvesSessionAndLogsIt(t *testing.T) {
	d, _, _, st := newTestDispatcherSeq(`{"action":"reply","params":{},"user_reply":"hi"}`)
	st.session = DispatchSession{ID: 42, Summary: "Earlier: discussed Tan policy.", SummaryThroughLogID: 7}
	st.tail = []DispatchTurn{{Message: "what next?", UserReply: "Renew in March.", ID: 8}}
	_ = d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})
	if st.resolvedChannel != "telegram" || st.resolvedOwner != "1" {
		t.Fatalf("session not resolved for owner: %q/%q", st.resolvedChannel, st.resolvedOwner)
	}
	if st.loggedSessionID != 42 {
		t.Fatalf("log not tagged with session id, got %d", st.loggedSessionID)
	}
}

func TestBuildPromptIncludesSummaryAndTail(t *testing.T) {
	sess := DispatchSession{Summary: "Rolling summary: client Tan wants renewal."}
	tail := []DispatchTurn{{Message: "remind me", UserReply: "Renewal due March."}}
	got := buildDispatchUserPrompt(DispatchInput{Channel: "telegram", OwnerID: "1", Message: "ok"}, sess, tail)
	if !strings.Contains(got, "Rolling summary: client Tan wants renewal.") {
		t.Fatalf("prompt missing summary: %q", got)
	}
	if !strings.Contains(got, "Renewal due March.") {
		t.Fatalf("prompt missing tail: %q", got)
	}
}
```

Extend `fakeStore` in dispatch_test.go — find its struct and method set, then add fields + the new interface methods, and update its existing `SaveDispatchLog` to the new signature:

```go
// add to the fakeStore struct:
//   session         DispatchSession
//   tail            []DispatchTurn
//   recallHits      []SessionSummaryHit
//   resolvedChannel string
//   resolvedOwner   string
//   loggedSessionID int64

func (f *fakeStore) SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error {
	f.loggedSessionID = sessionID
	return nil
}
func (f *fakeStore) ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (DispatchSession, error) {
	f.resolvedChannel, f.resolvedOwner = channel, ownerID
	return f.session, nil
}
func (f *fakeStore) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error) {
	return f.tail, nil
}
func (f *fakeStore) SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]SessionSummaryHit, error) {
	return f.recallHits, nil
}
```

Remove the old `fakeStore.RecentDispatchTurns` method and its old `SaveDispatchLog` (replaced above). If existing tests referenced `fakeStore` fields tied to the old methods, leave those fields but they go unused.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run 'TestRunResolvesSession|TestBuildPromptIncludesSummary'`
Expected: FAIL — `DispatchSession`/`SessionSummaryHit` undefined, `buildDispatchUserPrompt` signature mismatch, interface not satisfied.

- [ ] **Step 3: Implement**

In `dispatch.go`, add the session types near `DispatchTurn` and add an `ID` field to `DispatchTurn`:

```go
// DispatchTurn is one prior exchange used to give Claude conversational memory.
type DispatchTurn struct {
	ID        int64
	Message   string
	UserReply string
	CreatedAt time.Time
}

// DispatchSession is the active conversation session: its rolling summary and
// the log id that summary already covers.
type DispatchSession struct {
	ID                  int64
	Summary             string
	SummaryThroughLogID int64
}

// SessionSummaryHit is a recall match over past session summaries.
type SessionSummaryHit struct {
	SessionID int64
	Summary   string
	StartedAt time.Time
}
```

Replace the `DispatchStore` interface with:

```go
// DispatchStore captures the session + audit-log dependency. Real impl is
// *store.Store via main.dispatchStoreAdapter.
type DispatchStore interface {
	SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error
	ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (DispatchSession, error)
	SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error)
	SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]SessionSummaryHit, error)
}
```

Add the session gap constant near `memoryWindow` (and remove `memoryWindow`/`memoryTurns` if now unused — see note):

```go
// sessionGap: messages this far apart or more start a new session.
const sessionGap = time.Hour

// sessionTailLimit caps raw turns pulled after the rolling summary.
const sessionTailLimit = 12
```

Replace `recentTurns` with session resolution. In `Run`, replace:

```go
	recent := d.recentTurns(ctx, in)
	transcript := buildDispatchUserPrompt(in, recent)
```

with:

```go
	sess, tail := d.resolveMemory(ctx, in)
	transcript := buildDispatchUserPrompt(in, sess, tail)
```

Replace the `recentTurns` function with:

```go
// resolveMemory loads the active session and its uncompacted tail. On any store
// error it degrades to an empty session (the turn still works, just without memory).
func (d *Dispatcher) resolveMemory(ctx context.Context, in DispatchInput) (DispatchSession, []DispatchTurn) {
	if d.Store == nil {
		return DispatchSession{}, nil
	}
	sess, err := d.Store.ResolveSession(ctx, in.Channel, in.OwnerID, sessionGap)
	if err != nil {
		return DispatchSession{}, nil
	}
	tail, err := d.Store.SessionTail(ctx, sess.ID, sess.SummaryThroughLogID, sessionTailLimit)
	if err != nil {
		return sess, nil
	}
	d.curSession = sess.ID // remember for logging
	return sess, tail
}
```

Add a `curSession int64` field to the `Dispatcher` struct (near `Claude`/`Exec`/`Store`).

Replace `buildDispatchUserPrompt` with:

```go
// buildDispatchUserPrompt assembles the prompt: the session's rolling summary
// (if any), then the uncompacted tail (oldest first), then the current message.
func buildDispatchUserPrompt(in DispatchInput, sess DispatchSession, tail []DispatchTurn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Owner channel: %s\nOwner ID: %s\nAvailable actions: %s\n\n", in.Channel, in.OwnerID, actionCatalog)

	if strings.TrimSpace(sess.Summary) != "" {
		fmt.Fprintf(&b, "Summary of this conversation so far:\n%s\n\n", sess.Summary)
	}
	if len(tail) > 0 {
		b.WriteString("Recent turns (oldest first, for context — don't re-execute):\n")
		for _, t := range tail {
			fmt.Fprintf(&b, "Owner: %s\nYou: %s\n", t.Message, t.UserReply)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Owner message:\n%s", in.Message)
	return b.String()
}
```

Update `logAsync` to log the resolved session id:

```go
		_ = d.Store.SaveDispatchLog(bgCtx, d.curSession, in.Channel, in.OwnerID, in.Message, string(res.Action), res.UserReply, res.Error, dur.Milliseconds())
```

Note on `memoryWindow`/`memoryTurns`: if no longer referenced after this change, delete those constants to avoid an "unused" lint. (`SessionTail` replaces them.)

- [ ] **Step 4: Run the agent suite**

Run: `go test ./internal/agent/`
Expected: PASS — new tests + all existing (existing tests use `fakeStore`, now updated). `go build ./internal/agent/` clean. (`go build ./...` still fails until Task 6 updates main's adapter — expected.)

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(dispatch): session-aware memory (rolling summary + tail)"
```

---

### Task 4: `recall_memory` action

**Files:**
- Modify: `internal/agent/dispatch.go` (action const; execute signature + case; prompt/catalog)
- Modify: `internal/agent/dispatch_test.go`

- [ ] **Step 1: Write the failing test**

```go
// append to internal/agent/dispatch_test.go
func TestRunRecallMemory(t *testing.T) {
	d, _, _, st := newTestDispatcherSeq(
		`{"action":"recall_memory","params":{"query":"tan policy"},"user_reply":"checking","continue":true}`,
		`{"action":"reply","params":{},"user_reply":"We agreed to renew in March."}`,
	)
	st.recallHits = []SessionSummaryHit{{SessionID: 9, Summary: "Owner agreed to renew Tan policy in March."}}
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "what did we decide about tan?"})
	// The recalled summary must have been fed back into the next prompt.
	c := d.Claude.(*fakeClaude)
	if !strings.Contains(c.lastU, "renew Tan policy in March") {
		t.Fatalf("recalled summary not fed back: %q", c.lastU)
	}
	if !strings.Contains(res.UserReply, "March") {
		t.Fatalf("unexpected reply %q", res.UserReply)
	}
}

func TestRecallMemoryRequiresQuery(t *testing.T) {
	d, _, _, _ := newTestDispatcher(`{"action":"recall_memory","params":{},"user_reply":"x"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "recall"})
	if !strings.Contains(strings.ToLower(res.UserReply), "query") {
		t.Fatalf("expected a 'query required' style failure, got %q", res.UserReply)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run 'TestRunRecallMemory|TestRecallMemoryRequiresQuery'`
Expected: FAIL — `ActionRecallMemory` undefined / recall not handled.

- [ ] **Step 3: Implement**

Add the const to the `Action` block:

```go
	ActionRecallMemory Action = "recall_memory"
```

`recall_memory` reads session summaries from the store, scoped to the current owner — so it needs the channel/owner from `DispatchInput`. Change `execute` to take `in`:

- Update the signature: `func (d *Dispatcher) execute(ctx context.Context, in DispatchInput, p *dispatchPayload) (string, error) {`
- Update its single call site in `Run`: `status, exErr := d.execute(ctx, in, parsed)`

Add the case to the `execute` switch (before `default`):

```go
	case ActionRecallMemory:
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("recall_memory params: %w", err)
		}
		if strings.TrimSpace(args.Query) == "" {
			return "", errors.New("recall_memory: query required")
		}
		if d.Store == nil {
			return "(memory unavailable)", nil
		}
		hits, err := d.Store.SearchSessionSummaries(ctx, in.Channel, in.OwnerID, args.Query, 3)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matching past conversations)", nil
		}
		var sb strings.Builder
		for _, h := range hits {
			when := ""
			if !h.StartedAt.IsZero() {
				when = h.StartedAt.Format("2006-01-02") + ": "
			}
			fmt.Fprintf(&sb, "\n• %s%s", when, h.Summary)
		}
		return strings.TrimSpace(sb.String()), nil
```

Add to the system prompt schema enum (`... | "search_notes" | "recall_memory" | "reply"`) and the params list:

```
- recall_memory: {"query": "tan policy renewal"} — search summaries of your PAST conversations with this owner for relevant context. Use this (usually with continue:true) when the owner refers to something discussed earlier that isn't in the recent turns above.
```

Add `recall_memory` to `actionCatalog`.

- [ ] **Step 4: Run the agent suite**

Run: `go test ./internal/agent/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(dispatch): model-driven recall_memory action over session summaries"
```

---

### Task 5: `Compactor` background cron

**Files:**
- Create: `internal/agent/compactor.go`
- Create: `internal/agent/compactor_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/compactor_test.go
package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCompStore struct {
	idle      []DispatchSession
	tail      []DispatchTurn
	updated   bool
	gotID     int64
	gotSumm   string
	gotThrough int64
}

func (f *fakeCompStore) IdleSessionsToCompact(ctx context.Context, idle time.Duration, limit int) ([]DispatchSession, error) {
	return f.idle, nil
}
func (f *fakeCompStore) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error) {
	return f.tail, nil
}
func (f *fakeCompStore) UpdateSessionSummary(ctx context.Context, sessionID int64, summary string, throughLogID int64) error {
	f.updated, f.gotID, f.gotSumm, f.gotThrough = true, sessionID, summary, throughLogID
	return nil
}

func TestCompactorCompactsIdleSession(t *testing.T) {
	st := &fakeCompStore{
		idle: []DispatchSession{{ID: 5, Summary: "prior", SummaryThroughLogID: 2}},
		tail: []DispatchTurn{{ID: 3, Message: "hi", UserReply: "hello"}, {ID: 4, Message: "renew?", UserReply: "March"}},
	}
	c := &Compactor{Store: st, Summarizer: &fakeClaude{reply: "Updated summary: greeted, renewal in March."}}
	c.runOnce(context.Background())
	if !st.updated {
		t.Fatal("expected summary update")
	}
	if st.gotID != 5 || st.gotThrough != 4 {
		t.Fatalf("wrong session/through: %d/%d", st.gotID, st.gotThrough)
	}
	if !strings.Contains(st.gotSumm, "renewal in March") {
		t.Fatalf("summary not from summarizer: %q", st.gotSumm)
	}
}

func TestCompactorSkipsEmptyTail(t *testing.T) {
	st := &fakeCompStore{idle: []DispatchSession{{ID: 5, SummaryThroughLogID: 9}}, tail: nil}
	c := &Compactor{Store: st, Summarizer: &fakeClaude{reply: "x"}}
	c.runOnce(context.Background())
	if st.updated {
		t.Fatal("must not update a session with no new turns")
	}
}

func TestCompactorSummarizerErrorLeavesTail(t *testing.T) {
	st := &fakeCompStore{
		idle: []DispatchSession{{ID: 5, SummaryThroughLogID: 0}},
		tail: []DispatchTurn{{ID: 1, Message: "a", UserReply: "b"}},
	}
	c := &Compactor{Store: st, Summarizer: &fakeClaude{err: context.DeadlineExceeded}}
	c.runOnce(context.Background())
	if st.updated {
		t.Fatal("summarizer error must not advance the summary")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestCompactor`
Expected: FAIL — `Compactor`/`CompactorStore` undefined.

- [ ] **Step 3: Implement**

```go
// internal/agent/compactor.go
package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// CompactorStore is the slice of the store the Compactor needs.
type CompactorStore interface {
	IdleSessionsToCompact(ctx context.Context, idleThreshold time.Duration, limit int) ([]DispatchSession, error)
	SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error)
	UpdateSessionSummary(ctx context.Context, sessionID int64, summary string, throughLogID int64) error
}

// Compactor periodically folds idle sessions' new turns into their rolling
// summary, so an active conversation stays "infinite" but the prompt stays
// bounded. It runs ONLY against idle sessions (owner offline), never on the
// request path.
type Compactor struct {
	Store         CompactorStore
	Summarizer    ClaudeRunner // cheap model (haiku) — quality of summary is secondary
	Interval      time.Duration // tick cadence; default 10m
	IdleThreshold time.Duration // session idle for at least this long → compact; default 15m
	Logger        *log.Logger
}

const (
	defaultCompactInterval = 10 * time.Minute
	defaultCompactIdle     = 15 * time.Minute
	compactTailLimit       = 100
)

// Start launches the ticker loop in a goroutine; it stops when ctx is cancelled.
func (c *Compactor) Start(ctx context.Context) {
	if c.Interval <= 0 {
		c.Interval = defaultCompactInterval
	}
	if c.IdleThreshold <= 0 {
		c.IdleThreshold = defaultCompactIdle
	}
	go func() {
		t := time.NewTicker(c.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				c.runOnce(ctx)
			}
		}
	}()
}

// runOnce compacts every currently-idle session with uncompacted turns.
func (c *Compactor) runOnce(ctx context.Context) {
	if c.Store == nil || c.Summarizer == nil {
		return
	}
	sessions, err := c.Store.IdleSessionsToCompact(ctx, c.IdleThreshold, 20)
	if err != nil {
		c.logf("compactor: list idle: %v", err)
		return
	}
	for _, s := range sessions {
		tail, err := c.Store.SessionTail(ctx, s.ID, s.SummaryThroughLogID, compactTailLimit)
		if err != nil || len(tail) == 0 {
			continue
		}
		summary, err := c.summarize(ctx, s.Summary, tail)
		if err != nil {
			c.logf("compactor: summarize session %d: %v", s.ID, err)
			continue // leave the tail; retry next tick
		}
		through := tail[len(tail)-1].ID
		if err := c.Store.UpdateSessionSummary(ctx, s.ID, summary, through); err != nil {
			c.logf("compactor: update session %d: %v", s.ID, err)
		}
	}
}

const compactSystemPrompt = `You maintain a running summary of a conversation between an owner and their CRM assistant. Merge the existing summary with the new turns into a single concise summary (under 200 words). Keep concrete facts: names, decisions, dates, pending tasks, preferences. Drop chit-chat. Output only the summary text — no preamble.`

func (c *Compactor) summarize(ctx context.Context, prior string, tail []DispatchTurn) (string, error) {
	var b strings.Builder
	if strings.TrimSpace(prior) != "" {
		fmt.Fprintf(&b, "Existing summary:\n%s\n\n", prior)
	}
	b.WriteString("New turns (oldest first):\n")
	for _, t := range tail {
		fmt.Fprintf(&b, "Owner: %s\nAssistant: %s\n", t.Message, t.UserReply)
	}
	out, err := c.Summarizer.Reply(ctx, compactSystemPrompt, b.String())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c *Compactor) logf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger.Printf(format, args...)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -run TestCompactor` then `go test ./internal/agent/`
Expected: PASS. `go build ./internal/agent/` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/compactor.go internal/agent/compactor_test.go
git commit -m "feat(agent): SessionCompactor cron for idle-session summarization"
```

---

### Task 6: Wire store adapter + compactor in `main.go`

**Files:**
- Modify: `main.go` (`dispatchStoreAdapter` ~line 48-67; SaveDispatchLog signature already changed; dispatcher build ~line 675-690; add compactor startup)

- [ ] **Step 1: Update the store adapter**

Replace the `dispatchStoreAdapter` methods with the session-aware set (it must satisfy BOTH `agent.DispatchStore` and `agent.CompactorStore`):

```go
func (a *dispatchStoreAdapter) SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error {
	return a.s.SaveDispatchLog(ctx, sessionID, channel, ownerID, message, action, userReply, errText, durationMS)
}

func (a *dispatchStoreAdapter) ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (agent.DispatchSession, error) {
	r, err := a.s.ResolveSession(ctx, channel, ownerID, gap)
	if err != nil {
		return agent.DispatchSession{}, err
	}
	return agent.DispatchSession{ID: r.ID, Summary: r.Summary, SummaryThroughLogID: r.SummaryThroughLogID}, nil
}

func (a *dispatchStoreAdapter) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]agent.DispatchTurn, error) {
	entries, err := a.s.SessionTail(ctx, sessionID, sinceLogID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.DispatchTurn, len(entries))
	for i, e := range entries {
		out[i] = agent.DispatchTurn{ID: e.ID, Message: e.Message, UserReply: e.UserReply, CreatedAt: e.CreatedAt}
	}
	return out, nil
}

func (a *dispatchStoreAdapter) SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]agent.SessionSummaryHit, error) {
	hits, err := a.s.SearchSessionSummaries(ctx, channel, ownerID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.SessionSummaryHit, len(hits))
	for i, h := range hits {
		out[i] = agent.SessionSummaryHit{SessionID: h.SessionID, Summary: h.Summary, StartedAt: h.StartedAt}
	}
	return out, nil
}

func (a *dispatchStoreAdapter) IdleSessionsToCompact(ctx context.Context, idleThreshold time.Duration, limit int) ([]agent.DispatchSession, error) {
	rows, err := a.s.IdleSessionsToCompact(ctx, idleThreshold, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.DispatchSession, len(rows))
	for i, r := range rows {
		out[i] = agent.DispatchSession{ID: r.ID, Summary: r.Summary, SummaryThroughLogID: r.SummaryThroughLogID}
	}
	return out, nil
}
```

Remove the old `RecentDispatchTurns` adapter method (no longer in the interface). Confirm `time` is imported in main.go (it is).

- [ ] **Step 2: Build to verify adapter satisfies the interfaces**

Run: `go build ./...`
Expected: SUCCESS (the old `SaveDispatchLog`-caller breakage from Task 2 is now resolved, and the adapter satisfies both interfaces).

- [ ] **Step 3: Start the Compactor**

After the dispatcher is built and `agentRunner.SetDispatcher(dispatcher)` is called (~line 691), add:

```go
	// Background session compactor: folds idle conversations' turns into their
	// rolling summary (owner offline). Uses haiku (knowClient) — cheap; summary
	// quality is secondary. The "cron" the owner asked for, in-process.
	compactor := &agent.Compactor{
		Store:      &dispatchStoreAdapter{s: appStore},
		Summarizer: knowClient,
		Logger:     log.Default(),
	}
	compactor.Start(context.Background())
```

(`Interval`/`IdleThreshold` default to 10m/15m inside `Start`.)

- [ ] **Step 4: Build + full test suite**

Run: `go build ./... && go test ./...`
Expected: SUCCESS; all packages PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat(dispatch): wire session store adapter + start SessionCompactor"
```

---

### Task 7: Full build + manual smoke (session memory end-to-end)

**Files:** none.

- [ ] **Step 1: Build + suite**

Run: `go build ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: Live checks (running app + Telegram)**

1. **Continuity:** message the bot, then within an hour reference the earlier message ("what did I just ask?") — it should recall via the tail.
2. **Recall:** after a session has been compacted (or wait/force idle), start a fresh exchange and ask about an old topic ("what did we decide about Tan's policy?") — the bot should issue `recall_memory` (watch logs) and answer from the summary.
3. **Compaction:** leave a session idle ~15min; confirm in the DB that `dispatch_sessions.summary` is populated and `summary_through_log_id` advanced (`sqlite3 ~/.claude-bridge/<db> "SELECT id,summary,summary_through_log_id,last_at FROM dispatch_sessions;"`).

- [ ] **Step 3: Commit (only if smoke-test tweaks were needed)**

```bash
git add -A && git commit -m "chore(dispatch): phase-3 tweaks from smoke test"
```

(Skip if none. Use explicit paths — never bare `git add -A` across the repo.)

---

## Self-Review Notes

- **Spec coverage (§6, §7):** session keyed by (channel, owner) (Task 2 ResolveSession) ✓; 1h idle gap boundary (`sessionGap`, Task 3) ✓; rolling summary + raw tail prompt assembly (Task 3) ✓; `dispatch_sessions` schema + `summary_through_log_id` (Task 1) ✓; per-call session resolve so dashboard-independent (Task 3) ✓; `recall_memory` model-driven, owner-scoped, FTS5 (Tasks 2,4) ✓; `SessionCompactor` in-process ticker, idle-only (≥15m), haiku summarizer, prunes by advancing `summary_through_log_id`, best-effort retry on error (Tasks 5,6) ✓.
- **Placeholder scan:** none — complete code throughout.
- **Type consistency:** `DispatchSession{ID,Summary,SummaryThroughLogID}` and `SessionSummaryHit{SessionID,Summary,StartedAt}` and `DispatchTurn.ID` are defined in Task 3 and used identically in Tasks 4-6; store returns `DispatchSessionRow`/`SessionSummaryHit` (Task 1-2) mapped by the adapter (Task 6); `SaveDispatchLog(sessionID, ...)` new signature is defined in Task 2 and matched in the interface (Task 3), fakeStore (Task 3), adapter (Task 6), and `logAsync` call (Task 3). `CompactorStore` (Task 5) is satisfied by the adapter (Task 6).
- **Build-break sequencing (intentional):** Task 2 changes `SaveDispatchLog`'s signature, breaking `go build ./...` until Task 3 (dispatch call site + interface) and Task 6 (main adapter) land. Each task notes this; package-scoped `go test ./internal/store/` and `go test ./internal/agent/` stay green throughout, and the full build is restored in Task 6.
- **Known minor (carried from Phase 2 review, still deferred):** mid-chain executor errors aren't recorded in the audit `error_text`. Out of scope here.
- **Recall scoping:** `SearchSessionSummaries` filters by channel+owner so one owner can never recall another's history.
