package agent

import (
	"context"
	"log"
	"strings"
	"time"

	"claude-bridge/internal/store"
)

// IncomingMsg carries the data needed to generate a reply.
type IncomingMsg struct {
	ContactJID string
	AccountJID string
	Body       string
	PushName   string
	Timestamp  time.Time
}

// Runner processes incoming messages asynchronously and sends AI replies.
type Runner struct {
	incoming chan IncomingMsg
	replier  *Replier
	sender   func(phone, text, fromJID string) error
	store    *store.Store
}

// NewRunner creates a Runner. sender is wa.Manager.SendMessage.
func NewRunner(replier *Replier, sender func(phone, text, fromJID string) error, s *store.Store) *Runner {
	return &Runner{
		incoming: make(chan IncomingMsg, 100),
		replier:  replier,
		sender:   sender,
		store:    s,
	}
}

// Enqueue adds an incoming message to the processing queue (non-blocking).
func (r *Runner) Enqueue(contactJID, accountJID, body, pushName string, ts time.Time) {
	msg := IncomingMsg{
		ContactJID: contactJID,
		AccountJID: accountJID,
		Body:       body,
		PushName:   pushName,
		Timestamp:  ts,
	}
	select {
	case r.incoming <- msg:
	default:
		log.Printf("[agent] queue full, dropping message from %s", contactJID)
	}
}

// Start launches the background processing goroutine.
func (r *Runner) Start() {
	go r.loop()
}

func (r *Runner) loop() {
	for msg := range r.incoming {
		r.process(msg)
	}
}

func (r *Runner) process(msg IncomingMsg) {
	if msg.Body == "" || strings.HasSuffix(msg.ContactJID, "@g.us") {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	cfg, err := LoadConfig(ctx, r.store)
	if err != nil || !cfg.Enabled {
		return
	}

	reply, err := r.replier.Reply(ctx, cfg, msg.ContactJID, msg.Body)
	if err != nil {
		log.Printf("[agent] reply error for %s: %v", msg.ContactJID, err)
		return
	}
	if reply == "" {
		return
	}

	// Extract phone number from JID (e.g. "60123456789@s.whatsapp.net" → "60123456789")
	phone := strings.Split(msg.ContactJID, "@")[0]
	if err := r.sender(phone, reply, msg.AccountJID); err != nil {
		log.Printf("[agent] send error to %s: %v", phone, err)
		return
	}

	// Persist outgoing reply
	_ = r.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       "whatsapp",
		ConversationID: msg.ContactJID,
		MessageID:      "agent-" + msg.ContactJID + "-" + msg.Timestamp.Format("20060102150405"),
		SenderID:       msg.AccountJID,
		SenderName:     "Agent",
		Content:        reply,
		Timestamp:      time.Now(),
		IsOutgoing:     true,
	})

	preview := reply
	if len(preview) > 60 {
		preview = preview[:60]
	}
	log.Printf("[agent] replied to %s (%s): %s", msg.PushName, phone, preview)
}
