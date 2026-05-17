package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Action is a single dispatch verb. Adding a new action means: (1) const here,
// (2) handler in Dispatcher.execute, (3) entry in actionCatalog for the prompt.
type Action string

const (
	ActionSendWhatsApp   Action = "send_whatsapp"
	ActionBroadcast      Action = "broadcast_whatsapp"
	ActionSearchKB       Action = "search_kb"
	ActionListPending    Action = "list_pending"
	ActionSummaryInbox   Action = "summary_inbox"
	ActionListContacts   Action = "list_contacts"
	ActionGetProfile     Action = "get_profile"
	ActionUpdateProfile  Action = "update_profile"
	ActionExtractProfile Action = "extract_profile"
	ActionReply          Action = "reply"
)

// ClaudeRunner is the subset of *claude.Client used by the dispatcher.
type ClaudeRunner interface {
	Reply(ctx context.Context, systemPrompt, conversation string) (string, error)
}

// KBHit is a knowledge-base search hit (filename + snippet).
type KBHit struct {
	Filename string
	Summary  string
}

// PendingSummary is a one-line summary of a pending reply for dispatch output.
type PendingSummary struct {
	ID         int64
	ContactJID string
	Incoming   string
	Proposed   string
}

// InboxSummary buckets recent inbound activity by sender.
type InboxSummary struct {
	Sender     string
	JID        string
	Count      int
	LastBody   string
	LastWhenMS int64
}

// ContactSummary is one row for list_contacts output.
type ContactSummary struct {
	JID      string
	PushName string
	Platform string
}

// ProfileInfo is a flattened view of a client profile for dispatch output.
type ProfileInfo struct {
	JID         string
	DisplayName string
	Aliases     []string
	Language    string
	Role        string
	FamilyNotes string
	Interests   []string
	LastTopics  []string
	CustomNotes string
}

// ProfileQuery describes how to look up a profile — by JID OR by name.
type ProfileQuery struct {
	JID  string
	Name string
}

// Executor performs the resolved action. Real impl wraps wa + batch queue +
// store + profile extractor; tests inject a stub.
type Executor interface {
	SendWhatsAppMessage(ctx context.Context, phone, message, fromJID string) error
	BroadcastWhatsApp(ctx context.Context, recipients []string, message string) (batchID string, err error)
	SearchKB(ctx context.Context, query string, limit int) ([]KBHit, error)
	ListPendingReplies(ctx context.Context) ([]PendingSummary, error)
	SummarizeInbox(ctx context.Context, hours int) ([]InboxSummary, error)
	ListContacts(ctx context.Context, search string, limit int) (rows []ContactSummary, total int, err error)
	ResolveContact(ctx context.Context, query string) ([]ContactSummary, error)
	GetProfile(ctx context.Context, q ProfileQuery) (*ProfileInfo, error)
	UpdateProfile(ctx context.Context, jid, field, value string) error
	ExtractProfile(ctx context.Context, jid string) (*ProfileInfo, error)
}

// DispatchStore captures the audit-log + memory dependency. Real impl is
// *store.Store. Action is passed as a plain string so the store package
// doesn't need to import the agent package for the type.
type DispatchStore interface {
	SaveDispatchLog(ctx context.Context, channel, ownerID, message, action, userReply, errText string, durationMS int64) error
	RecentDispatchTurns(ctx context.Context, channel, ownerID string, since time.Duration, limit int) ([]DispatchTurn, error)
}

// DispatchTurn is one prior exchange used to give Claude conversational memory.
type DispatchTurn struct {
	Message   string
	UserReply string
	CreatedAt time.Time
}

// DispatchInput is one owner-originated request.
type DispatchInput struct {
	Channel string // "telegram" or "whatsapp"
	OwnerID string // tg id (stringified) or whatsapp jid
	Message string
}

// DispatchResult is what the dispatcher hands back to the connector to reply.
type DispatchResult struct {
	Action    Action
	UserReply string
	Error     string
}

// Dispatcher resolves owner free-text into a structured action and runs it.
type Dispatcher struct {
	Claude ClaudeRunner
	Exec   Executor
	Store  DispatchStore
}

// NewDispatcher builds a Dispatcher. Any field may be nil; nil Store skips the
// audit log, nil Claude/Exec causes Run to return a clear error.
func NewDispatcher(c ClaudeRunner, ex Executor, st DispatchStore) *Dispatcher {
	return &Dispatcher{Claude: c, Exec: ex, Store: st}
}

// systemPrompt is the dispatch agent's behavioral spec. Kept here (not in the
// agent config's SystemPrompt) because it dictates the JSON output schema —
// changing it changes the wire contract between Claude and the executor.
const dispatchSystemPrompt = `You are the owner's dispatch agent. The owner messages you to take real actions on their CRM system.

Respond with ONE JSON object only — no preamble, no markdown fences. Schema:

{
  "action": "send_whatsapp" | "broadcast_whatsapp" | "search_kb" | "list_pending" | "summary_inbox" | "reply",
  "params": { ... },
  "user_reply": "Short status to send back to the owner (1-2 sentences)."
}

Action params:

- send_whatsapp: {"phone": "60123456789", "name": "Alice", "message": "Hi Alice ..."} — single WhatsApp send. Provide EITHER phone OR name. If only a name is given (typical for the owner), the executor resolves it against contacts. Ambiguous matches come back with a list so you can ask the user which one to pick.
- broadcast_whatsapp: {"recipients": ["60111...", "60222..."], "message": "..."} — paced bulk send via the batch queue.
- search_kb: {"query": "policy renewal", "limit": 5} — search the knowledge base.
- list_pending: {} — list pending replies awaiting owner review.
- summary_inbox: {"hours": 24} — summarize who messaged the owner recently.
- list_contacts: {"search": "alice", "limit": 20} — list known contacts; search is an optional substring against name/phone, limit defaults to 20 (max 50).
- get_profile: {"jid": "601...", "name": "Alice"} — lookup client profile by JID or name (at least one).
- update_profile: {"jid": "601...", "field": "custom_notes", "value": "VIP, prefers WhatsApp"} — edit one profile field. Allowed fields: display_name, role, language, family_notes, custom_notes.
- extract_profile: {"jid": "601..."} — run Claude over recent messages to refresh the profile.
- reply: {} — just chat, no side effect.

Rules:
1. If the owner asks for something destructive or large-scale (>20 recipients), reply with action "reply" and ask for explicit confirmation. Do not dispatch directly.
2. Never invent phone numbers or JIDs. If the owner says "send to Alice" without a number, return action "reply" asking which number.
3. Keep user_reply short. The executor will append a status line ("Sent." / "3 results.") after your reply.
4. If unsure which action fits, default to "reply" with your best guess at help text.`

// actionCatalog is included in the user prompt for quick reference. Keep in
// sync with dispatchSystemPrompt schema.
const actionCatalog = `send_whatsapp, broadcast_whatsapp, search_kb, list_pending, summary_inbox, reply`

// memoryWindow is how far back to pull prior turns when building the prompt
// for context. Keep small to limit prompt growth.
const (
	memoryWindow = 30 * time.Minute
	memoryTurns  = 5
)

// Run executes one dispatch turn. Returns the reply to send back to the owner.
func (d *Dispatcher) Run(ctx context.Context, in DispatchInput) DispatchResult {
	start := time.Now()
	res := DispatchResult{Action: ActionReply}

	if d.Claude == nil {
		res.Error = "dispatcher: no Claude runner configured"
		res.UserReply = "Dispatch is offline."
		d.logAsync(ctx, in, res, time.Since(start))
		return res
	}

	recent := d.recentTurns(ctx, in)
	user := buildDispatchUserPrompt(in, recent)

	raw, err := d.Claude.Reply(ctx, dispatchSystemPrompt, user)
	if err != nil {
		res.Error = err.Error()
		res.UserReply = "Sorry — dispatch failed: " + truncate(err.Error(), 200)
		d.logAsync(ctx, in, res, time.Since(start))
		return res
	}

	parsed, err := parseDispatch(raw)
	if err != nil {
		// Fallback: treat the entire raw output as a reply. Better to surface
		// Claude's text than to fail loudly when the JSON is slightly off.
		res.Action = ActionReply
		res.UserReply = strings.TrimSpace(raw)
		if res.UserReply == "" {
			res.UserReply = "I'm not sure what to do with that."
		}
		d.logAsync(ctx, in, res, time.Since(start))
		return res
	}

	res.Action = parsed.Action
	res.UserReply = parsed.UserReply

	if d.Exec == nil {
		res.Error = "no executor configured"
		res.UserReply = parsed.UserReply + " (executor offline — action skipped)"
		d.logAsync(ctx, in, res, time.Since(start))
		return res
	}

	status, err := d.execute(ctx, parsed)
	if err != nil {
		res.Error = err.Error()
		res.UserReply = parsed.UserReply + " (failed: " + truncate(err.Error(), 100) + ")"
	} else if status != "" {
		res.UserReply = strings.TrimSpace(parsed.UserReply + " " + status)
	}

	d.logAsync(ctx, in, res, time.Since(start))
	return res
}

// dispatchPayload is the parsed JSON Claude returns.
type dispatchPayload struct {
	Action    Action          `json:"action"`
	Params    json.RawMessage `json:"params"`
	UserReply string          `json:"user_reply"`
}

// parseDispatch extracts a dispatchPayload from raw Claude output. Tolerant of
// surrounding markdown fences or stray prefix text.
func parseDispatch(raw string) (*dispatchPayload, error) {
	s := strings.TrimSpace(raw)
	// Strip markdown code fences if present.
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// If there's still prefix junk, find the first { and last } to slice the
	// JSON region.
	if !strings.HasPrefix(s, "{") {
		start := strings.Index(s, "{")
		end := strings.LastIndex(s, "}")
		if start >= 0 && end > start {
			s = s[start : end+1]
		}
	}

	var p dispatchPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	if p.Action == "" {
		return nil, errors.New("missing action")
	}
	return &p, nil
}

// execute runs the resolved action and returns a status suffix appended to
// user_reply. Returns (status, error). status is omitted when empty.
func (d *Dispatcher) execute(ctx context.Context, p *dispatchPayload) (string, error) {
	switch p.Action {
	case ActionReply:
		return "", nil

	case ActionSendWhatsApp:
		var args struct {
			Phone   string `json:"phone"`
			Name    string `json:"name"`
			Message string `json:"message"`
			FromJID string `json:"from_jid"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("send_whatsapp params: %w", err)
		}
		if args.Message == "" {
			return "", errors.New("send_whatsapp: message required")
		}
		phone := args.Phone
		display := args.Phone
		if phone == "" {
			if args.Name == "" {
				return "", errors.New("send_whatsapp: phone or name required")
			}
			matches, err := d.Exec.ResolveContact(ctx, args.Name)
			if err != nil {
				return "", err
			}
			switch len(matches) {
			case 0:
				return "(no contact matching '" + args.Name + "' — try a different name or use list_contacts)", nil
			case 1:
				phone = shortJID(matches[0].JID)
				display = matches[0].PushName
				if display == "" {
					display = phone
				}
			default:
				var sb strings.Builder
				fmt.Fprintf(&sb, "(%d contacts match '%s' — be more specific:", len(matches), args.Name)
				for i, m := range matches {
					if i >= 5 {
						sb.WriteString("\n  …")
						break
					}
					sb.WriteString("\n  • ")
					sb.WriteString(displayOrJID(m.PushName, m.JID))
					sb.WriteString(" (")
					sb.WriteString(shortJID(m.JID))
					sb.WriteString(")")
				}
				sb.WriteString(")")
				return sb.String(), nil
			}
		}
		if err := d.Exec.SendWhatsAppMessage(ctx, phone, args.Message, args.FromJID); err != nil {
			return "", err
		}
		return "(sent to " + display + ")", nil

	case ActionBroadcast:
		var args struct {
			Recipients []string `json:"recipients"`
			Message    string   `json:"message"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("broadcast_whatsapp params: %w", err)
		}
		if len(args.Recipients) == 0 || args.Message == "" {
			return "", errors.New("broadcast_whatsapp: recipients and message required")
		}
		id, err := d.Exec.BroadcastWhatsApp(ctx, args.Recipients, args.Message)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(broadcast %s — %d recipients)", id, len(args.Recipients)), nil

	case ActionSearchKB:
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("search_kb params: %w", err)
		}
		if args.Query == "" {
			return "", errors.New("search_kb: query required")
		}
		if args.Limit <= 0 || args.Limit > 10 {
			args.Limit = 5
		}
		hits, err := d.Exec.SearchKB(ctx, args.Query, args.Limit)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matches)", nil
		}
		var sb strings.Builder
		for _, h := range hits {
			sb.WriteString("\n• ")
			sb.WriteString(h.Filename)
			if h.Summary != "" {
				sb.WriteString(" — ")
				sb.WriteString(truncate(h.Summary, 120))
			}
		}
		return sb.String(), nil

	case ActionListPending:
		pendings, err := d.Exec.ListPendingReplies(ctx)
		if err != nil {
			return "", err
		}
		if len(pendings) == 0 {
			return "(none)", nil
		}
		var sb strings.Builder
		for _, p := range pendings {
			sb.WriteString(fmt.Sprintf("\n#%d %s: %s", p.ID, shortJID(p.ContactJID), truncate(p.Incoming, 60)))
		}
		return sb.String(), nil

	case ActionListContacts:
		var args struct {
			Search string `json:"search"`
			Limit  int    `json:"limit"`
		}
		_ = json.Unmarshal(p.Params, &args)
		if args.Limit <= 0 {
			args.Limit = 20
		}
		if args.Limit > 50 {
			args.Limit = 50
		}
		rows, total, err := d.Exec.ListContacts(ctx, args.Search, args.Limit)
		if err != nil {
			return "", err
		}
		if total == 0 {
			return "(no contacts)", nil
		}
		var sb strings.Builder
		if args.Search != "" {
			fmt.Fprintf(&sb, "(%d match, showing %d)", total, len(rows))
		} else {
			fmt.Fprintf(&sb, "(%d total, showing %d)", total, len(rows))
		}
		for _, c := range rows {
			sb.WriteString("\n• ")
			sb.WriteString(displayOrJID(c.PushName, c.JID))
			sb.WriteString(" — ")
			sb.WriteString(shortJID(c.JID))
		}
		return sb.String(), nil

	case ActionGetProfile:
		var args struct {
			JID  string `json:"jid"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("get_profile params: %w", err)
		}
		if args.JID == "" && args.Name == "" {
			return "", errors.New("get_profile: jid or name required")
		}
		info, err := d.Exec.GetProfile(ctx, ProfileQuery{JID: args.JID, Name: args.Name})
		if err != nil {
			return "", err
		}
		if info == nil {
			return "(no profile)", nil
		}
		return formatProfile(info), nil

	case ActionUpdateProfile:
		var args struct {
			JID   string `json:"jid"`
			Field string `json:"field"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("update_profile params: %w", err)
		}
		if args.JID == "" || args.Field == "" {
			return "", errors.New("update_profile: jid and field required")
		}
		if err := d.Exec.UpdateProfile(ctx, args.JID, args.Field, args.Value); err != nil {
			return "", err
		}
		return "(updated " + args.Field + ")", nil

	case ActionExtractProfile:
		var args struct {
			JID string `json:"jid"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("extract_profile params: %w", err)
		}
		if args.JID == "" {
			return "", errors.New("extract_profile: jid required")
		}
		info, err := d.Exec.ExtractProfile(ctx, args.JID)
		if err != nil {
			return "", err
		}
		if info == nil {
			return "(no messages to extract from)", nil
		}
		return "(profile refreshed)\n" + formatProfile(info), nil

	case ActionSummaryInbox:
		var args struct {
			Hours int `json:"hours"`
		}
		_ = json.Unmarshal(p.Params, &args)
		if args.Hours <= 0 || args.Hours > 168 {
			args.Hours = 24
		}
		buckets, err := d.Exec.SummarizeInbox(ctx, args.Hours)
		if err != nil {
			return "", err
		}
		if len(buckets) == 0 {
			return "(quiet)", nil
		}
		var sb strings.Builder
		for _, b := range buckets {
			sb.WriteString(fmt.Sprintf("\n• %s (%d): %s", displayOrJID(b.Sender, b.JID), b.Count, truncate(b.LastBody, 80)))
		}
		return sb.String(), nil

	default:
		return "", fmt.Errorf("unknown action %q", p.Action)
	}
}

// recentTurns fetches conversational memory for this owner+channel. Returns
// nil if memory is unavailable (no store, query error, etc) — memory is
// best-effort, never fatal.
func (d *Dispatcher) recentTurns(ctx context.Context, in DispatchInput) []DispatchTurn {
	if d.Store == nil {
		return nil
	}
	turns, err := d.Store.RecentDispatchTurns(ctx, in.Channel, in.OwnerID, memoryWindow, memoryTurns)
	if err != nil {
		return nil
	}
	return turns
}

// buildDispatchUserPrompt assembles the user-side prompt: prior turns (oldest
// first) then the current message.
func buildDispatchUserPrompt(in DispatchInput, recent []DispatchTurn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Owner channel: %s\nOwner ID: %s\nAvailable actions: %s\n\n", in.Channel, in.OwnerID, actionCatalog)

	if len(recent) > 0 {
		b.WriteString("Recent conversation (oldest first, for context — don't re-execute):\n")
		// store returns newest-first; reverse for chronological display.
		for i := len(recent) - 1; i >= 0; i-- {
			t := recent[i]
			fmt.Fprintf(&b, "Owner: %s\nYou: %s\n", t.Message, t.UserReply)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Owner message:\n%s", in.Message)
	return b.String()
}

// logAsync writes to the audit log without blocking the caller. Errors are
// logged but never returned — audit failures shouldn't break dispatch.
func (d *Dispatcher) logAsync(ctx context.Context, in DispatchInput, res DispatchResult, dur time.Duration) {
	if d.Store == nil {
		return
	}
	go func() {
		// Detach from caller ctx (which may already be cancelled by the time
		// we reach this goroutine after a network reply).
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Store.SaveDispatchLog(bgCtx, in.Channel, in.OwnerID, in.Message, string(res.Action), res.UserReply, res.Error, dur.Milliseconds())
	}()
}

// formatProfile renders a ProfileInfo as a compact multi-line string for
// dispatch output. Empty fields are skipped to keep the reply tight.
func formatProfile(p *ProfileInfo) string {
	var b strings.Builder
	name := p.DisplayName
	if name == "" {
		name = shortJID(p.JID)
	}
	b.WriteString("\n" + name)
	if p.Role != "" {
		b.WriteString(" — " + p.Role)
	}
	if len(p.Aliases) > 0 {
		b.WriteString("\nAliases: " + strings.Join(p.Aliases, ", "))
	}
	if p.Language != "" {
		b.WriteString("\nLanguage: " + p.Language)
	}
	if p.FamilyNotes != "" {
		b.WriteString("\nFamily: " + p.FamilyNotes)
	}
	if len(p.Interests) > 0 {
		b.WriteString("\nInterests: " + strings.Join(p.Interests, ", "))
	}
	if len(p.LastTopics) > 0 {
		b.WriteString("\nRecent topics: " + strings.Join(p.LastTopics, ", "))
	}
	if p.CustomNotes != "" {
		b.WriteString("\nNotes: " + p.CustomNotes)
	}
	return b.String()
}

// shortJID strips the @s.whatsapp.net suffix for compact display.
var jidSuffixRE = regexp.MustCompile(`@[^@]+$`)

func shortJID(j string) string {
	return jidSuffixRE.ReplaceAllString(j, "")
}

func displayOrJID(name, jid string) string {
	if name != "" {
		return name
	}
	return shortJID(jid)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
