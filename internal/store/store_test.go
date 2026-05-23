package store

import (
	"context"
	"testing"
	"time"
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
