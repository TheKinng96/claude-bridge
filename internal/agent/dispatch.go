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
	ActionListCowork     Action = "list_cowork"
	ActionReadCowork     Action = "read_cowork"
	ActionSearchCowork   Action = "search_cowork"
	ActionEditCowork     Action = "edit_cowork"
	ActionCreateCowork   Action = "create_cowork"
	ActionCoworkPath     Action = "cowork_path"
	ActionListKB         Action = "list_kb"
	ActionReadKB         Action = "read_kb"
	ActionReadNote       Action = "read_note"
	ActionBacklinks      Action = "backlinks"
	ActionSearchNotes        Action = "search_notes"
	ActionRecallMemory       Action = "recall_memory"
	ActionGetOwnerProfile    Action = "get_owner_profile"
	ActionUpdateOwnerProfile Action = "update_owner_profile"
	ActionReply              Action = "reply"
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

// CoworkFile is a flattened view of a cowork-folder file for dispatch output.
type CoworkFile struct {
	Name    string
	Date    string // YYYY-MM-DD folder
	Size    int64
	IsText  bool
	ModTime time.Time
}

// CoworkHit is one search match inside a cowork text file.
type CoworkHit struct {
	Date    string
	Name    string
	Line    int
	Snippet string
}

// CoworkRead is the payload returned by ReadCowork.
type CoworkRead struct {
	File    CoworkFile
	Content string
}

// KBEntry is one file/dir in the raw knowledge-base folder.
type KBEntry struct {
	Name    string
	RelPath string
	Size    int64
	IsDir   bool
	IsText  bool
}

// NoteView is a flattened Obsidian note for dispatch output.
type NoteView struct {
	Name        string
	RelPath     string
	Frontmatter map[string]string
	Body        string
	OutLinks    []string
	Tags        []string
}

// NoteHit is one Obsidian search match.
type NoteHit struct {
	Note    string
	Line    int
	Snippet string
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
	ListCowork(ctx context.Context, date string) ([]CoworkFile, error)
	ReadCowork(ctx context.Context, filename string) (*CoworkRead, error)
	SearchCowork(ctx context.Context, query string, days int) ([]CoworkHit, error)
	EditCowork(ctx context.Context, filename, op, content string) (*CoworkFile, error)
	CreateCowork(ctx context.Context, date, filename, content string) (*CoworkFile, error)
	CoworkPath(ctx context.Context, date string) (string, error)
	ListKB(ctx context.Context, subdir string) ([]KBEntry, error)
	ReadKB(ctx context.Context, path string) (string, *KBEntry, error)
	ReadNote(ctx context.Context, name string) (*NoteView, error)
	Backlinks(ctx context.Context, name string) ([]string, error)
	SearchNotes(ctx context.Context, query, tag string) ([]NoteHit, error)
	GetOwnerProfile(ctx context.Context) (string, error)
	UpdateOwnerProfile(ctx context.Context, content, mode string) error
}

// DispatchStore captures the session + audit-log dependency. Real impl is
// *store.Store via main.dispatchStoreAdapter.
type DispatchStore interface {
	SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error
	ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (DispatchSession, error)
	SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]DispatchTurn, error)
	SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]SessionSummaryHit, error)
}

// DispatchTurn is one prior exchange used to give Claude conversational memory.
type DispatchTurn struct {
	ID        int64
	Message   string
	UserReply string
	CreatedAt time.Time
}

// DispatchSession is the active conversation session: its rolling summary and
// the log id that summary already covers.
type DispatchSession struct {
	ID                  int64
	Summary             string
	SummaryThroughLogID int64
}

// SessionSummaryHit is a recall match over past session summaries.
type SessionSummaryHit struct {
	SessionID int64
	Summary   string
	StartedAt time.Time
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
  "action": "send_whatsapp" | "broadcast_whatsapp" | "search_kb" | "list_pending" | "summary_inbox" | "list_contacts" | "get_profile" | "update_profile" | "extract_profile" | "list_cowork" | "read_cowork" | "search_cowork" | "edit_cowork" | "create_cowork" | "cowork_path" | "list_kb" | "read_kb" | "read_note" | "backlinks" | "search_notes" | "recall_memory" | "get_owner_profile" | "update_owner_profile" | "reply",
  "params": { ... },
  "user_reply": "Short status to send back to the owner (1-2 sentences).",
  "continue": false
}

CHAINING: You run in a loop. Set "continue": true when you need this action's
result before deciding the next step — for example read_kb a file, then
send_whatsapp a contact about what it said. When continue is true the action
runs, you are shown its result, and you must issue the next action (or "reply").
Set continue to false (or omit it) for a single action whose result can go
straight to the owner. Always finish a multi-step task with action "reply".

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
- list_cowork: {"date": "today" | "yesterday" | "YYYY-MM-DD"} — list files inside the cowork folder for that date. Defaults to today when date is empty. Cowork files are outputs from routines (drafts, generated images, JSON dumps) plus anything the owner dropped into the folder by hand.
- read_cowork: {"filename": "draft_tan.md" | "2026-05-19/draft_tan.md"} — read a file from the dated COWORK output folders specifically (routine drafts/images filed by recent date). Bare names fuzzy-match within the last 14 days, newest wins. For a file the owner just names without saying it's a cowork output, prefer read_kb. Binary files (png/pdf) come back as a placeholder.
- search_cowork: {"query": "tan ws", "days": 7} — grep across text files in recent date folders (days defaults to 7, max 30).
- edit_cowork: {"filename": "draft_tan.md", "op": "append" | "replace", "content": "..."} — modify an existing text file. Use "append" to add a line/block; "replace" rewrites the entire file. Binary files cannot be edited.
- create_cowork: {"filename": "notes_tan.md", "date": "today" | "YYYY-MM-DD", "content": "..."} — create a NEW text file (date defaults to today, ".md" added if no extension). Fails if a file of that name already exists — use edit_cowork to modify existing files.
- cowork_path: {"date": "today" | "yesterday" | "YYYY-MM-DD"} — return (and create) the absolute path of the cowork folder for that date. Use when the owner asks where files go, or where a routine should write its output.
- list_kb: {"subdir": ""} — list files/folders in the shared knowledge-base folder. Empty subdir lists the top level; pass a relative subfolder to drill in.
- read_kb: {"path": "weather.txt" | "sub/report.md"} — read a file from the knowledge-base folder. A bare filename is searched for across the WHOLE folder (including the Vault/ and Cowork/ subfolders), newest match wins — so use this for any file the owner names without giving a path. A path containing a slash is read exactly. Binary files return a placeholder.
- read_note: {"name": "Alice" | "Clients/Alice" | "[[Alice]]"} — read an Obsidian note: body plus its outgoing [[links]] and #tags.
- backlinks: {"name": "Tan Policy"} — list notes that link TO the named note.
- search_notes: {"query": "renewal", "tag": "vip"} — search Obsidian notes by text and/or #tag (at least one). Empty query with a tag lists all notes carrying that tag.
- recall_memory: {"query": "tan policy renewal"} — search summaries of your PAST conversations with this owner for relevant context. Use when the owner refers to something discussed earlier that isn't in the recent turns above. ALWAYS set "continue": true on recall_memory so you can read the results and answer the owner in your own words with a follow-up "reply".
- get_owner_profile: {} — return the owner's saved profile/persona document.
- update_owner_profile: {"content": "...", "mode": "replace" | "append"} — save the owner's profile (mode defaults to replace). Use when the owner asks to set/update their profile. To edit, first get_owner_profile (continue:true), merge, then update_owner_profile with the full new text.
- reply: {} — just chat, no side effect.

Rules:
1. If the owner asks for something destructive or large-scale (>20 recipients), reply with action "reply" and ask for explicit confirmation. Do not dispatch directly.
2. Never invent phone numbers or JIDs. If the owner says "send to Alice" without a number, return action "reply" asking which number.
3. Keep user_reply short. For a single action (continue false), the action's result is appended to your user_reply automatically — EXCEPT send_whatsapp, where nothing is appended, so write user_reply as the complete past-tense confirmation, e.g. "Sent to Ana (60123456789).". For a continue:true chain, intermediate user_reply text is NOT sent — only your final "reply" message reaches the owner.
4. If unsure which action fits, default to "reply" with your best guess at help text.`

// actionCatalog is included in the user prompt for quick reference. Keep in
// sync with dispatchSystemPrompt schema.
const actionCatalog = `send_whatsapp, broadcast_whatsapp, search_kb, list_pending, summary_inbox, list_contacts, get_profile, update_profile, extract_profile, list_cowork, read_cowork, search_cowork, edit_cowork, create_cowork, cowork_path, list_kb, read_kb, read_note, backlinks, search_notes, recall_memory, get_owner_profile, update_owner_profile, reply`

// sessionGap: messages this far apart or more start a new session.
const sessionGap = time.Hour

// sessionTailLimit caps raw turns pulled after the rolling summary.
const sessionTailLimit = 12

const (
	maxDispatchSteps = 5    // bound on chained actions per owner message
	observationLimit = 6000 // chars of an action result fed back into the prompt
)

// Run executes one owner message. By default it resolves and runs a single
// action (one-shot, result appended to the reply). When the model sets
// "continue": true on an action, Run feeds that action's result back and asks
// for the next action, looping up to maxDispatchSteps or until the model emits
// "reply". Only the final message is sent to the owner; chained turns get a
// compact action trail appended for transparency.
func (d *Dispatcher) Run(ctx context.Context, in DispatchInput) DispatchResult {
	start := time.Now()
	res := DispatchResult{Action: ActionReply}

	if d.Claude == nil {
		res.Error = "dispatcher: no Claude runner configured"
		res.UserReply = "Dispatch is offline."
		d.logAsync(ctx, in, res, time.Since(start), 0)
		return res
	}

	sess, tail := d.resolveMemory(ctx, in)
	transcript := buildDispatchUserPrompt(in, sess, tail)
	var trail []string

	sysPrompt := d.ownerProfileSystemPrompt(ctx)

	for step := 0; step < maxDispatchSteps; step++ {
		raw, err := d.Claude.Reply(ctx, sysPrompt, transcript)
		if err != nil {
			res.Error = err.Error()
			res.UserReply = "Sorry — dispatch failed: " + truncate(err.Error(), 200)
			d.logAsync(ctx, in, res, time.Since(start), sess.ID)
			return res
		}

		parsed, perr := parseDispatch(raw)
		if perr != nil {
			// Tolerant fallback: surface raw text as the reply.
			res.Action = ActionReply
			res.UserReply = strings.TrimSpace(raw)
			if res.UserReply == "" {
				res.UserReply = "I'm not sure what to do with that."
			}
			break
		}

		// reply ends the loop with the model's message.
		if parsed.Action == ActionReply {
			res.Action = ActionReply
			res.UserReply = parsed.UserReply
			res.Error = ""
			break
		}

		if d.Exec == nil {
			res.Action = parsed.Action
			res.UserReply = parsed.UserReply + " (executor offline — action skipped)"
			res.Error = "no executor configured"
			d.logAsync(ctx, in, res, time.Since(start), sess.ID)
			return res
		}

		status, exErr := d.execute(ctx, in, parsed)
		trail = append(trail, string(parsed.Action))
		res.Action = parsed.Action

		// Chain only when the model asked to continue AND a step remains.
		// Otherwise behave one-shot: append this action's result and stop.
		if !parsed.Continue || step == maxDispatchSteps-1 {
			switch {
			case exErr != nil:
				res.Error = exErr.Error()
				res.UserReply = parsed.UserReply + " (failed: " + truncate(exErr.Error(), 100) + ")"
			case parsed.Action == ActionSendWhatsApp:
				// The model's reply already confirms the send; the executor's
				// "(sent to …)" status duplicates it (and clashes in tense),
				// so trust the reply and drop the status on success.
				res.UserReply = strings.TrimSpace(parsed.UserReply)
			case status != "":
				res.UserReply = strings.TrimSpace(parsed.UserReply + " " + status)
			default:
				res.UserReply = parsed.UserReply
			}
			break
		}

		// Continue: feed the result back for the next step.
		obs := status
		if exErr != nil {
			obs = "ERROR: " + exErr.Error()
		} else if obs == "" {
			obs = "(done)"
		}
		transcript += fmt.Sprintf(
			"\n\n[You ran action: %s]\n[Result]: %s\n\nContinue with the next action, or respond with action \"reply\" and a short message once the owner's request is fully handled.",
			parsed.Action, truncate(obs, observationLimit),
		)
	}

	// Append a compact action trail only when the turn chained 2+ actions.
	if len(trail) > 1 {
		res.UserReply = strings.TrimSpace(res.UserReply) + "\n· " + strings.Join(trail, " → ")
	}

	d.logAsync(ctx, in, res, time.Since(start), sess.ID)
	return res
}

// ownerProfileSystemPrompt returns dispatchSystemPrompt, optionally prefixed
// with the owner's profile so the bot adopts it as its persona. A missing or
// failed profile read degrades silently to the base prompt — the profile is
// context, never a hard dependency.
func (d *Dispatcher) ownerProfileSystemPrompt(ctx context.Context) string {
	if d.Exec == nil {
		return dispatchSystemPrompt
	}
	profile, err := d.Exec.GetOwnerProfile(ctx)
	if err != nil || strings.TrimSpace(profile) == "" {
		return dispatchSystemPrompt
	}
	return "OWNER PROFILE (your persona and context — honor it, but ALWAYS obey the OUTPUT SCHEMA below):\n" +
		strings.TrimSpace(profile) + "\n\n" + dispatchSystemPrompt
}

// dispatchPayload is the parsed JSON Claude returns.
type dispatchPayload struct {
	Action    Action          `json:"action"`
	Params    json.RawMessage `json:"params"`
	UserReply string          `json:"user_reply"`
	Continue  bool            `json:"continue"` // true → run this action, feed result back, expect a follow-up action
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
func (d *Dispatcher) execute(ctx context.Context, in DispatchInput, p *dispatchPayload) (string, error) {
	switch p.Action {
	case ActionReply:
		return "", nil

	case ActionRecallMemory:
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("recall_memory params: %w", err)
		}
		if strings.TrimSpace(args.Query) == "" {
			return "", errors.New("recall_memory: query required")
		}
		if d.Store == nil {
			return "(memory unavailable)", nil
		}
		hits, err := d.Store.SearchSessionSummaries(ctx, in.Channel, in.OwnerID, args.Query, 3)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matching past conversations)", nil
		}
		var sb strings.Builder
		for _, h := range hits {
			when := ""
			if !h.StartedAt.IsZero() {
				when = h.StartedAt.Format("2006-01-02") + ": "
			}
			fmt.Fprintf(&sb, "\n• %s%s", when, h.Summary)
		}
		return strings.TrimSpace(sb.String()), nil

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

	case ActionListCowork:
		var args struct {
			Date string `json:"date"`
		}
		_ = json.Unmarshal(p.Params, &args)
		files, err := d.Exec.ListCowork(ctx, args.Date)
		if err != nil {
			return "", err
		}
		if len(files) == 0 {
			return "(no files)", nil
		}
		return formatCoworkList(args.Date, files), nil

	case ActionReadCowork:
		var args struct {
			Filename string `json:"filename"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("read_cowork params: %w", err)
		}
		if strings.TrimSpace(args.Filename) == "" {
			return "", errors.New("read_cowork: filename required")
		}
		out, err := d.Exec.ReadCowork(ctx, args.Filename)
		if err != nil {
			return "", err
		}
		if out == nil {
			return "(no file)", nil
		}
		header := fmt.Sprintf("\n[%s/%s, %d bytes]\n", out.File.Date, out.File.Name, out.File.Size)
		return header + out.Content, nil

	case ActionSearchCowork:
		var args struct {
			Query string `json:"query"`
			Days  int    `json:"days"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("search_cowork params: %w", err)
		}
		if strings.TrimSpace(args.Query) == "" {
			return "", errors.New("search_cowork: query required")
		}
		hits, err := d.Exec.SearchCowork(ctx, args.Query, args.Days)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matches)", nil
		}
		var sb strings.Builder
		for _, h := range hits {
			fmt.Fprintf(&sb, "\n• %s/%s:%d — %s", h.Date, h.Name, h.Line, truncate(h.Snippet, 100))
		}
		return sb.String(), nil

	case ActionEditCowork:
		var args struct {
			Filename string `json:"filename"`
			Op       string `json:"op"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("edit_cowork params: %w", err)
		}
		if strings.TrimSpace(args.Filename) == "" {
			return "", errors.New("edit_cowork: filename required")
		}
		if args.Content == "" {
			return "", errors.New("edit_cowork: content required")
		}
		entry, err := d.Exec.EditCowork(ctx, args.Filename, args.Op, args.Content)
		if err != nil {
			return "", err
		}
		op := args.Op
		if op == "" {
			op = "append"
		}
		return fmt.Sprintf("(%s %s/%s, now %d bytes)", op, entry.Date, entry.Name, entry.Size), nil

	case ActionCreateCowork:
		var args struct {
			Date     string `json:"date"`
			Filename string `json:"filename"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("create_cowork params: %w", err)
		}
		if strings.TrimSpace(args.Filename) == "" {
			return "", errors.New("create_cowork: filename required")
		}
		entry, err := d.Exec.CreateCowork(ctx, args.Date, args.Filename, args.Content)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(created %s/%s, %d bytes)", entry.Date, entry.Name, entry.Size), nil

	case ActionCoworkPath:
		var args struct {
			Date string `json:"date"`
		}
		_ = json.Unmarshal(p.Params, &args)
		path, err := d.Exec.CoworkPath(ctx, args.Date)
		if err != nil {
			return "", err
		}
		return "\n" + path, nil

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

	case ActionListKB:
		var args struct {
			Subdir string `json:"subdir"`
		}
		_ = json.Unmarshal(p.Params, &args)
		entries, err := d.Exec.ListKB(ctx, args.Subdir)
		if err != nil {
			return "", err
		}
		if len(entries) == 0 {
			return "(folder is empty)", nil
		}
		var sb strings.Builder
		for i, e := range entries {
			if i >= 30 {
				fmt.Fprintf(&sb, "\n…(+%d more)", len(entries)-30)
				break
			}
			kind := "📄"
			if e.IsDir {
				kind = "📁"
			}
			label := e.RelPath
			if label == "" {
				label = e.Name
			}
			fmt.Fprintf(&sb, "\n%s %s", kind, label)
		}
		return strings.TrimSpace(sb.String()), nil

	case ActionReadKB:
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("read_kb params: %w", err)
		}
		if strings.TrimSpace(args.Path) == "" {
			return "", errors.New("read_kb: path required")
		}
		content, _, err := d.Exec.ReadKB(ctx, args.Path)
		if err != nil {
			return "", err
		}
		return truncate(content, 3500), nil

	case ActionReadNote:
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("read_note params: %w", err)
		}
		if strings.TrimSpace(args.Name) == "" {
			return "", errors.New("read_note: name required")
		}
		note, err := d.Exec.ReadNote(ctx, args.Name)
		if err != nil {
			return "", err
		}
		if note == nil {
			return "(note not found)", nil
		}
		var sb strings.Builder
		sb.WriteString(note.Body)
		if len(note.OutLinks) > 0 {
			fmt.Fprintf(&sb, "\n\nLinks: %s", strings.Join(note.OutLinks, ", "))
		}
		if len(note.Tags) > 0 {
			fmt.Fprintf(&sb, "\nTags: #%s", strings.Join(note.Tags, " #"))
		}
		return truncate(strings.TrimSpace(sb.String()), 3500), nil

	case ActionBacklinks:
		var args struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("backlinks params: %w", err)
		}
		if strings.TrimSpace(args.Name) == "" {
			return "", errors.New("backlinks: name required")
		}
		links, err := d.Exec.Backlinks(ctx, args.Name)
		if err != nil {
			return "", err
		}
		if len(links) == 0 {
			return "(no backlinks)", nil
		}
		return "Linked from: " + strings.Join(links, ", "), nil

	case ActionSearchNotes:
		var args struct {
			Query string `json:"query"`
			Tag   string `json:"tag"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("search_notes params: %w", err)
		}
		if strings.TrimSpace(args.Query) == "" && strings.TrimSpace(args.Tag) == "" {
			return "", errors.New("search_notes: query or tag required")
		}
		hits, err := d.Exec.SearchNotes(ctx, args.Query, args.Tag)
		if err != nil {
			return "", err
		}
		if len(hits) == 0 {
			return "(no matching notes)", nil
		}
		var sb strings.Builder
		for i, h := range hits {
			if i >= 15 {
				fmt.Fprintf(&sb, "\n…(+%d more)", len(hits)-15)
				break
			}
			fmt.Fprintf(&sb, "\n• %s: %s", h.Note, h.Snippet)
		}
		return strings.TrimSpace(sb.String()), nil

	case ActionGetOwnerProfile:
		profile, err := d.Exec.GetOwnerProfile(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(profile) == "" {
			return "(no profile set yet)", nil
		}
		return "\n" + profile, nil

	case ActionUpdateOwnerProfile:
		var args struct {
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("update_owner_profile params: %w", err)
		}
		if strings.TrimSpace(args.Content) == "" {
			return "", errors.New("update_owner_profile: content required")
		}
		if err := d.Exec.UpdateOwnerProfile(ctx, args.Content, args.Mode); err != nil {
			return "", err
		}
		mode := args.Mode
		if mode == "" {
			mode = "replace"
		}
		return "(profile " + mode + "d)", nil

	default:
		return "", fmt.Errorf("unknown action %q", p.Action)
	}
}

// resolveMemory loads the active session and its uncompacted tail. On any store
// error it degrades to an empty session (the turn still works, just without
// memory).
func (d *Dispatcher) resolveMemory(ctx context.Context, in DispatchInput) (DispatchSession, []DispatchTurn) {
	if d.Store == nil {
		return DispatchSession{}, nil
	}
	sess, err := d.Store.ResolveSession(ctx, in.Channel, in.OwnerID, sessionGap)
	if err != nil {
		return DispatchSession{}, nil
	}
	tail, err := d.Store.SessionTail(ctx, sess.ID, sess.SummaryThroughLogID, sessionTailLimit)
	if err != nil {
		return sess, nil
	}
	return sess, tail
}

// buildDispatchUserPrompt assembles the prompt: the session's rolling summary
// (if any), then the uncompacted tail (oldest first), then the current message.
func buildDispatchUserPrompt(in DispatchInput, sess DispatchSession, tail []DispatchTurn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Owner channel: %s\nOwner ID: %s\nAvailable actions: %s\n\n", in.Channel, in.OwnerID, actionCatalog)

	if strings.TrimSpace(sess.Summary) != "" {
		fmt.Fprintf(&b, "Summary of this conversation so far:\n%s\n\n", sess.Summary)
	}
	if len(tail) > 0 {
		b.WriteString("Recent turns (oldest first, for context — don't re-execute):\n")
		for _, t := range tail {
			fmt.Fprintf(&b, "Owner: %s\nYou: %s\n", t.Message, t.UserReply)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Owner message:\n%s", in.Message)
	return b.String()
}

// logAsync writes to the audit log without blocking the caller. Errors are
// logged but never returned — audit failures shouldn't break dispatch.
func (d *Dispatcher) logAsync(ctx context.Context, in DispatchInput, res DispatchResult, dur time.Duration, sessionID int64) {
	if d.Store == nil {
		return
	}
	go func() {
		// Detach from caller ctx (which may already be cancelled by the time
		// we reach this goroutine after a network reply).
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Store.SaveDispatchLog(bgCtx, sessionID, in.Channel, in.OwnerID, in.Message, string(res.Action), res.UserReply, res.Error, dur.Milliseconds())
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

// formatCoworkList renders the list_cowork result with a date header and one
// line per file (newest first). Truncates to 15 entries to fit Telegram.
func formatCoworkList(date string, files []CoworkFile) string {
	if date == "" {
		date = "today"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "(%d file(s) on %s)", len(files), date)
	const maxRows = 15
	for i, f := range files {
		if i >= maxRows {
			fmt.Fprintf(&sb, "\n  …+%d more", len(files)-maxRows)
			break
		}
		kind := "txt"
		if !f.IsText {
			kind = "bin"
		}
		fmt.Fprintf(&sb, "\n• [%s] %s — %dB", kind, f.Name, f.Size)
	}
	return sb.String()
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
