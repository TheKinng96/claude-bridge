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
		HasHistory:  true,
		LastInbound: time.Now().Add(-7 * 24 * time.Hour),
	})
	if got != TierActive {
		t.Fatalf("got %s, want %s", got, TierActive)
	}
}

func TestClassify_OldReply_IsQuiet(t *testing.T) {
	got := Classify(ContactStats{
		HasHistory:  true,
		LastInbound: time.Now().Add(-90 * 24 * time.Hour),
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
