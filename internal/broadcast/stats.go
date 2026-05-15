package broadcast

import (
	"context"
	"time"
)

// statsSource lets us inject mocks in tests; satisfied by *store.Store.
type statsSource interface {
	LastInboundAt(ctx context.Context, jid string) (time.Time, error)
	HasMessageHistory(ctx context.Context, jid string) (bool, error)
}

// FetchStats queries the store and returns the ContactStats for a JID.
func FetchStats(ctx context.Context, src statsSource, jid string) (ContactStats, error) {
	has, err := src.HasMessageHistory(ctx, jid)
	if err != nil {
		return ContactStats{}, err
	}
	last, err := src.LastInboundAt(ctx, jid)
	if err != nil {
		return ContactStats{}, err
	}
	return ContactStats{HasHistory: has, LastInbound: last}, nil
}
