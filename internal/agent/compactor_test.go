package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCompStore struct {
	idle       []DispatchSession
	tail       []DispatchTurn
	updated    bool
	gotID      int64
	gotSumm    string
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
