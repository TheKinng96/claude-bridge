package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
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
	incoming       chan IncomingMsg
	replier        *Replier
	sender         func(phone, text, fromJID string) error
	store          *store.Store
	lastNotifyTime time.Time
	notifyMu       sync.Mutex
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

	// Always upsert the contact so it appears in the Contacts tab.
	_, _ = r.store.UpsertContact(ctx, msg.ContactJID, "whatsapp", msg.PushName)

	cfg, err := LoadConfig(ctx, r.store)
	if err != nil {
		return
	}

	// !login command handled separately in Task 7 — placeholder check here
	if strings.TrimSpace(strings.ToLower(msg.Body)) == "!login" {
		if cfg.OwnerJID != "" && msg.ContactJID == cfg.OwnerJID {
			r.handleLoginCommand(ctx, cfg, msg)
		}
		return
	}

	if !cfg.Enabled {
		return
	}

	mode := ResolveReplyMode(ctx, r.store, msg.ContactJID, cfg.GlobalReplyMode)

	switch mode {
	case "off":
		return
	case "review":
		r.createPendingReply(ctx, cfg, msg)
	default: // "auto"
		r.autoReply(ctx, cfg, msg)
	}
}

func (r *Runner) autoReply(ctx context.Context, cfg Config, msg IncomingMsg) {
	reply, err := r.replier.Reply(ctx, cfg, msg.ContactJID, msg.Body)
	if err != nil {
		log.Printf("[agent] reply error for %s: %v", msg.ContactJID, err)
		return
	}
	if reply == "" {
		return
	}
	phone := strings.Split(msg.ContactJID, "@")[0]
	if err := r.sender(phone, reply, msg.AccountJID); err != nil {
		log.Printf("[agent] send error to %s: %v", phone, err)
		return
	}
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
	log.Printf("[agent] replied to %s: %s", msg.PushName, preview)
}

func (r *Runner) createPendingReply(ctx context.Context, cfg Config, msg IncomingMsg) {
	reply, err := r.replier.Reply(ctx, cfg, msg.ContactJID, msg.Body)
	if err != nil {
		log.Printf("[agent] pending reply generation error for %s: %v", msg.ContactJID, err)
		return
	}
	if reply == "" {
		return
	}
	if _, err := r.store.CreatePendingReply(ctx, msg.ContactJID, msg.AccountJID, "whatsapp", msg.Body, reply); err != nil {
		log.Printf("[agent] create pending reply error: %v", err)
		return
	}
	log.Printf("[agent] pending reply queued for %s (%s)", msg.PushName, msg.ContactJID)
	r.sendOwnerNotification(ctx, cfg, msg.AccountJID)
}

func (r *Runner) sendOwnerNotification(ctx context.Context, cfg Config, accountJID string) {
	if cfg.OwnerJID == "" {
		return
	}
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	if time.Since(r.lastNotifyTime) < 5*time.Minute {
		return
	}
	pending, err := r.store.ListPendingReplies(ctx, "pending")
	if err != nil || len(pending) == 0 {
		return
	}
	ownerPhone := strings.Split(cfg.OwnerJID, "@")[0]
	word := "replies"
	if len(pending) == 1 {
		word = "reply"
	}
	text := fmt.Sprintf("You have %d pending %s waiting. Review: http://127.0.0.1:10002/messages", len(pending), word)
	if err := r.sender(ownerPhone, text, accountJID); err != nil {
		log.Printf("[agent] owner notification error: %v", err)
		return
	}
	r.lastNotifyTime = time.Now()
}

func (r *Runner) handleLoginCommand(ctx context.Context, cfg Config, msg IncomingMsg) {
	// Implemented in Task 7
}
