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
