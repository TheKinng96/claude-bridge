package broadcast

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStatsSource struct {
	hasHistory  bool
	lastInbound time.Time
	hasErr      error
	lastErr     error
}

func (f *fakeStatsSource) HasMessageHistory(ctx context.Context, jid string) (bool, error) {
	return f.hasHistory, f.hasErr
}
func (f *fakeStatsSource) LastInboundAt(ctx context.Context, jid string) (time.Time, error) {
	return f.lastInbound, f.lastErr
}

func TestFetchStats_PopulatesBoth(t *testing.T) {
	now := time.Now()
	src := &fakeStatsSource{hasHistory: true, lastInbound: now}
	got, err := FetchStats(context.Background(), src, "1234@s.whatsapp.net")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got.HasHistory {
		t.Fatalf("HasHistory: want true")
	}
	if !got.LastInbound.Equal(now) {
		t.Fatalf("LastInbound: got %v, want %v", got.LastInbound, now)
	}
}

func TestFetchStats_PropagatesHistoryError(t *testing.T) {
	src := &fakeStatsSource{hasErr: errors.New("boom")}
	_, err := FetchStats(context.Background(), src, "x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchStats_PropagatesLastInboundError(t *testing.T) {
	src := &fakeStatsSource{hasHistory: true, lastErr: errors.New("boom")}
	_, err := FetchStats(context.Background(), src, "x")
	if err == nil {
		t.Fatal("expected error")
	}
}
