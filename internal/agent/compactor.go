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
	Summarizer    ClaudeRunner  // cheap model (haiku) — summary quality is secondary
	Interval      time.Duration // tick cadence; default 10m
	IdleThreshold time.Duration // session idle at least this long → compact; default 15m
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
