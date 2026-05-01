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
