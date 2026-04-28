package agent

import (
	"context"

	"claude-bridge/internal/store"
)

// ResolveReplyMode returns the effective reply mode for a contact.
// Priority: manual group > auto group > globalMode fallback.
// GetContactGroups already returns manual groups first.
func ResolveReplyMode(ctx context.Context, s *store.Store, contactJID, globalMode string) string {
	contact, err := s.GetContact(ctx, contactJID)
	if err != nil || contact == nil {
		return globalMode
	}
	groups, err := s.GetContactGroups(ctx, contact.ID)
	if err != nil || len(groups) == 0 {
		return globalMode
	}
	if groups[0].ReplyMode == "" {
		return globalMode
	}
	return groups[0].ReplyMode
}
