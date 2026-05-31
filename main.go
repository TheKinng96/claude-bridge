package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // registers /debug/pprof/* on DefaultServeMux; only served when --pprof is set
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"claude-bridge/internal/agent"
	"claude-bridge/internal/batch"
	"claude-bridge/internal/broadcast"
	"claude-bridge/internal/browser"
	"claude-bridge/internal/claude"
	"claude-bridge/internal/connectors/facebook"
	"claude-bridge/internal/connectors/telegram"
	"claude-bridge/internal/connectors/whatsapp"
	"claude-bridge/internal/cowork"
	"claude-bridge/internal/folderread"
	"claude-bridge/internal/intake"
	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/mcp"
	"claude-bridge/internal/obsidian"
	"claude-bridge/internal/ownerprofile"
	"claude-bridge/internal/profile"
	"claude-bridge/internal/server"
	"claude-bridge/internal/store"
	"claude-bridge/internal/tray"
	"claude-bridge/internal/updater"
)

// allowedProfileFields restricts which client_profile columns the dispatcher
// can overwrite via update_profile, preventing Claude from blanking out the
// JID, timestamps, or the auto-extracted aliases/interests arrays.
var allowedProfileFields = map[string]bool{
	"display_name": true,
	"role":         true,
	"language":     true,
	"family_notes": true,
	"custom_notes": true,
}

// dispatchStoreAdapter implements agent.DispatchStore by translating between
// store.DispatchLogEntry and the smaller agent.DispatchTurn shape so the
// agent package doesn't need to import store.
type dispatchStoreAdapter struct{ s *store.Store }

func (a *dispatchStoreAdapter) SaveDispatchLog(ctx context.Context, sessionID int64, channel, ownerID, message, action, userReply, errText string, durationMS int64) error {
	return a.s.SaveDispatchLog(ctx, sessionID, channel, ownerID, message, action, userReply, errText, durationMS)
}

func (a *dispatchStoreAdapter) ResolveSession(ctx context.Context, channel, ownerID string, gap time.Duration) (agent.DispatchSession, error) {
	r, err := a.s.ResolveSession(ctx, channel, ownerID, gap)
	if err != nil {
		return agent.DispatchSession{}, err
	}
	return agent.DispatchSession{ID: r.ID, Summary: r.Summary, SummaryThroughLogID: r.SummaryThroughLogID}, nil
}

func (a *dispatchStoreAdapter) SessionTail(ctx context.Context, sessionID, sinceLogID int64, limit int) ([]agent.DispatchTurn, error) {
	entries, err := a.s.SessionTail(ctx, sessionID, sinceLogID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.DispatchTurn, len(entries))
	for i, e := range entries {
		out[i] = agent.DispatchTurn{ID: e.ID, Message: e.Message, UserReply: e.UserReply, CreatedAt: e.CreatedAt}
	}
	return out, nil
}

func (a *dispatchStoreAdapter) SearchSessionSummaries(ctx context.Context, channel, ownerID, query string, limit int) ([]agent.SessionSummaryHit, error) {
	hits, err := a.s.SearchSessionSummaries(ctx, channel, ownerID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.SessionSummaryHit, len(hits))
	for i, h := range hits {
		out[i] = agent.SessionSummaryHit{SessionID: h.SessionID, Summary: h.Summary, StartedAt: h.StartedAt}
	}
	return out, nil
}

func (a *dispatchStoreAdapter) IdleSessionsToCompact(ctx context.Context, idleThreshold time.Duration, limit int) ([]agent.DispatchSession, error) {
	rows, err := a.s.IdleSessionsToCompact(ctx, idleThreshold, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.DispatchSession, len(rows))
	for i, r := range rows {
		out[i] = agent.DispatchSession{ID: r.ID, Summary: r.Summary, SummaryThroughLogID: r.SummaryThroughLogID}
	}
	return out, nil
}

func (a *dispatchStoreAdapter) UpdateSessionSummary(ctx context.Context, sessionID int64, summary string, throughLogID int64) error {
	return a.s.UpdateSessionSummary(ctx, sessionID, summary, throughLogID)
}

// dispatchExecutor implements agent.Executor by bridging Dispatcher actions to
// the WhatsApp connector, batch queue, SQLite store, and profile extractor.
type dispatchExecutor struct {
	wa           *whatsapp.Manager
	bq           *batch.Queue
	store        *store.Store
	extractor    *profile.Extractor
	obsidian     *obsidian.Writer // nil-safe — Writer methods no-op when path is empty
	cowork       *cowork.Root     // nil-safe via Enabled() — empty vault returns ErrDisabled
	ownerProfile *ownerprofile.Store
}

// syncObsidian writes the profile to the Obsidian vault if a writer is wired.
// Failures are logged but never returned — Obsidian is non-critical.
func (e *dispatchExecutor) syncObsidian(p *store.ClientProfile) {
	if e.obsidian == nil || p == nil {
		return
	}
	if err := e.obsidian.WriteClient(p); err != nil {
		log.Printf("[obsidian] write client failed: %v", err)
	}
	if err := e.obsidian.WriteTopicStubs(p.LastTopics); err != nil {
		log.Printf("[obsidian] write topics failed: %v", err)
	}
}

func (e *dispatchExecutor) SendWhatsAppMessage(ctx context.Context, phone, message, fromJID string) error {
	return e.wa.SendMessage(phone, message, fromJID)
}

// SendWhatsAppImage resolves image as a cowork-folder lookup (bare filename or
// "YYYY-MM-DD/name.png"), then sends the absolute file path via WhatsApp.
// Mirrors read_cowork's path resolution so routine outputs can be sent without
// the LLM needing to know absolute paths.
func (e *dispatchExecutor) SendWhatsAppImage(ctx context.Context, phone, image, caption, fromJID string) error {
	if e.cowork == nil || !e.cowork.Enabled() {
		return fmt.Errorf("cowork folder not configured — set it on the Knowledge tab to attach images")
	}
	entry, err := e.cowork.Find(image)
	if err != nil {
		return fmt.Errorf("locate image %q: %w", image, err)
	}
	if entry == nil {
		return fmt.Errorf("no cowork file matches %q", image)
	}
	return e.wa.SendImage(phone, entry.Path, caption, fromJID)
}

func (e *dispatchExecutor) BroadcastWhatsApp(ctx context.Context, recipients []string, message string) (string, error) {
	items := make([]map[string]string, 0, len(recipients))
	for _, phone := range recipients {
		items = append(items, map[string]string{
			"phone":   phone,
			"message": message,
		})
	}
	// Conservative default for dispatch-initiated broadcasts. The dashboard
	// path runs per-recipient tier classification; dispatch dispatches in bulk
	// without classification, so pick the medium safe range.
	minD, maxD := broadcast.DelayFor(broadcast.TierQuiet)
	id := e.bq.Submit("whatsapp", batch.JobSendMessage, items, minD, maxD)
	return id, nil
}

func (e *dispatchExecutor) SearchKB(ctx context.Context, query string, limit int) ([]agent.KBHit, error) {
	hits, err := e.store.SearchDocuments(ctx, query, "", "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]agent.KBHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.KBHit{Filename: h.Document.Filename, Summary: h.Document.Summary})
	}
	return out, nil
}

func (e *dispatchExecutor) ListPendingReplies(ctx context.Context) ([]agent.PendingSummary, error) {
	rows, err := e.store.ListPendingReplies(ctx, "pending")
	if err != nil {
		return nil, err
	}
	out := make([]agent.PendingSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, agent.PendingSummary{
			ID:         r.ID,
			ContactJID: r.ContactJID,
			Incoming:   r.IncomingMsg,
			Proposed:   r.ProposedReply,
		})
	}
	return out, nil
}

// ResolveContact looks up contacts matching query. Matches by name substring
// (case-insensitive) OR by phone digits (strips +, -, spaces, parens). Phone
// match looks at the JID's local part for s.whatsapp.net contacts.
func (e *dispatchExecutor) ResolveContact(ctx context.Context, query string) ([]agent.ContactSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	nameNeedle := strings.ToLower(query)
	phoneNeedle := digitsOnly(query)
	live := e.wa.GetContacts()
	out := make([]agent.ContactSummary, 0, 4)
	for _, c := range live {
		match := false
		if strings.Contains(strings.ToLower(c.PushName), nameNeedle) ||
			strings.Contains(strings.ToLower(c.JID), nameNeedle) {
			match = true
		}
		if !match && phoneNeedle != "" && strings.HasSuffix(c.JID, "@s.whatsapp.net") {
			// JID local part = phone digits for s.whatsapp.net contacts.
			local := strings.SplitN(c.JID, "@", 2)[0]
			if strings.Contains(digitsOnly(local), phoneNeedle) {
				match = true
			}
		}
		if match {
			out = append(out, agent.ContactSummary{
				JID:      c.JID,
				PushName: c.PushName,
				Platform: "whatsapp",
			})
		}
	}
	return out, nil
}

// digitsOnly strips everything except 0-9 from s.
func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (e *dispatchExecutor) ListContacts(ctx context.Context, search string, limit int) ([]agent.ContactSummary, int, error) {
	// Read from whatsmeow's live contact store — the full WhatsApp roster.
	// store.cached_contacts is only seeded on inbound messages, so it
	// undercounts the user's actual roster (60 vs 100+).
	live := e.wa.GetContacts()
	needle := strings.ToLower(strings.TrimSpace(search))
	matched := make([]agent.ContactSummary, 0, len(live))
	for _, c := range live {
		if needle != "" {
			hay := strings.ToLower(c.PushName + " " + c.JID)
			if !strings.Contains(hay, needle) {
				continue
			}
		}
		matched = append(matched, agent.ContactSummary{
			JID:      c.JID,
			PushName: c.PushName,
			Platform: "whatsapp",
		})
	}
	// Sort by name (case-insensitive) for stable presentation.
	sort.Slice(matched, func(i, j int) bool {
		return strings.ToLower(matched[i].PushName) < strings.ToLower(matched[j].PushName)
	})
	total := len(matched)
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

func (e *dispatchExecutor) GetProfile(ctx context.Context, q agent.ProfileQuery) (*agent.ProfileInfo, error) {
	var p *store.ClientProfile
	var err error
	if q.JID != "" {
		p, err = e.store.GetClientProfile(ctx, q.JID)
	} else if q.Name != "" {
		p, err = e.store.FindClientProfileByName(ctx, q.Name)
	}
	if err != nil {
		return nil, err
	}
	if p != nil {
		return profileToInfo(p), nil
	}
	// No client_profiles row — fall back to the live WhatsApp contact so the
	// owner at least gets a name for the number/JID they asked about.
	query := q.JID
	if query == "" {
		query = q.Name
	}
	matches, _ := e.ResolveContact(ctx, query)
	if len(matches) == 0 {
		return nil, nil
	}
	c := matches[0]
	return &agent.ProfileInfo{
		JID:         c.JID,
		DisplayName: c.PushName,
		CustomNotes: "(no profile yet — name from WhatsApp contact only)",
	}, nil
}

func (e *dispatchExecutor) UpdateProfile(ctx context.Context, jid, field, value string) error {
	if !allowedProfileFields[field] {
		return fmt.Errorf("field %q is not editable via dispatch", field)
	}
	existing, err := e.store.GetClientProfile(ctx, jid)
	if err != nil {
		return err
	}
	if existing == nil {
		existing = &store.ClientProfile{JID: jid}
	}
	switch field {
	case "display_name":
		existing.DisplayName = value
	case "role":
		existing.Role = value
	case "language":
		existing.Language = value
	case "family_notes":
		existing.FamilyNotes = value
	case "custom_notes":
		existing.CustomNotes = value
	}
	if err := e.store.UpsertClientProfile(ctx, existing); err != nil {
		return err
	}
	e.syncObsidian(existing)
	return nil
}

func (e *dispatchExecutor) ExtractProfile(ctx context.Context, jid string) (*agent.ProfileInfo, error) {
	if e.extractor == nil {
		return nil, fmt.Errorf("extractor not configured")
	}
	// Pull the last 30 inbound messages for context. GetCachedMessages returns
	// newest-first; reverse for chronological.
	msgs, _, err := e.store.GetCachedMessages(ctx, "whatsapp", jid, 30)
	if err != nil {
		return nil, err
	}
	inbound := make([]store.CachedMessage, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].IsOutgoing {
			inbound = append(inbound, msgs[i])
		}
	}
	if len(inbound) == 0 {
		return nil, nil
	}
	existing, _ := e.store.GetClientProfile(ctx, jid)
	updated, err := e.extractor.Extract(ctx, jid, existing, inbound)
	if err != nil {
		return nil, err
	}
	if err := e.store.UpsertClientProfile(ctx, updated); err != nil {
		return nil, err
	}
	e.syncObsidian(updated)
	return profileToInfo(updated), nil
}

// profileToInfo flattens a store.ClientProfile into the agent-level view.
func profileToInfo(p *store.ClientProfile) *agent.ProfileInfo {
	if p == nil {
		return nil
	}
	return &agent.ProfileInfo{
		JID:         p.JID,
		DisplayName: p.DisplayName,
		Aliases:     p.Aliases,
		Language:    p.Language,
		Role:        p.Role,
		FamilyNotes: p.FamilyNotes,
		Interests:   p.Interests,
		LastTopics:  p.LastTopics,
		CustomNotes: p.CustomNotes,
	}
}

func (e *dispatchExecutor) SummarizeInbox(ctx context.Context, hours int) ([]agent.InboxSummary, error) {
	cfg, _ := agent.LoadConfig(ctx, e.store)
	rows, err := e.store.SummarizeInbox(ctx, hours, cfg.OwnerJIDs)
	if err != nil {
		return nil, err
	}
	out := make([]agent.InboxSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, agent.InboxSummary{
			Sender:     r.PushName,
			JID:        r.JID,
			Count:      r.Count,
			LastBody:   r.LastBody,
			LastWhenMS: r.LastWhenMS,
		})
	}
	return out, nil
}

// ListCowork lists files in the dated cowork folder under the Obsidian vault.
func (e *dispatchExecutor) ListCowork(ctx context.Context, date string) ([]agent.CoworkFile, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return nil, fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	rows, err := e.cowork.List(date)
	if err != nil {
		return nil, err
	}
	out := make([]agent.CoworkFile, 0, len(rows))
	for _, r := range rows {
		out = append(out, agent.CoworkFile{
			Name: r.Name, Date: r.Date, Size: r.Size, IsText: r.IsText, ModTime: r.ModTime,
		})
	}
	return out, nil
}

func (e *dispatchExecutor) ReadCowork(ctx context.Context, filename string) (*agent.CoworkRead, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return nil, fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	content, entry, err := e.cowork.Read(filename)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	return &agent.CoworkRead{
		File: agent.CoworkFile{
			Name: entry.Name, Date: entry.Date, Size: entry.Size, IsText: entry.IsText, ModTime: entry.ModTime,
		},
		Content: content,
	}, nil
}

func (e *dispatchExecutor) SearchCowork(ctx context.Context, query string, days int) ([]agent.CoworkHit, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return nil, fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	hits, err := e.cowork.Search(query, days)
	if err != nil {
		return nil, err
	}
	out := make([]agent.CoworkHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.CoworkHit{Date: h.Date, Name: h.Name, Line: h.Line, Snippet: h.Snippet})
	}
	return out, nil
}

func (e *dispatchExecutor) CoworkPath(ctx context.Context, date string) (string, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return "", fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	return e.cowork.EnsureDate(date)
}

func (e *dispatchExecutor) EditCowork(ctx context.Context, filename, op, content string) (*agent.CoworkFile, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return nil, fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	entry, err := e.cowork.Edit(filename, content, cowork.EditOp(op))
	if err != nil {
		return nil, err
	}
	return &agent.CoworkFile{
		Name: entry.Name, Date: entry.Date, Size: entry.Size, IsText: entry.IsText, ModTime: entry.ModTime,
	}, nil
}

func (e *dispatchExecutor) CreateCowork(ctx context.Context, date, filename, content string) (*agent.CoworkFile, error) {
	if e.cowork == nil || !e.cowork.Enabled() {
		return nil, fmt.Errorf("cowork: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	entry, err := e.cowork.Create(date, filename, content)
	if err != nil {
		return nil, err
	}
	return &agent.CoworkFile{
		Name: entry.Name, Date: entry.Date, Size: entry.Size, IsText: entry.IsText, ModTime: entry.ModTime,
	}, nil
}

func (e *dispatchExecutor) GetOwnerProfile(ctx context.Context) (string, error) {
	if e.ownerProfile == nil || !e.ownerProfile.Enabled() {
		return "", nil // no vault → empty profile, bot uses base prompt
	}
	return e.ownerProfile.Read()
}

func (e *dispatchExecutor) UpdateOwnerProfile(ctx context.Context, content, mode string) error {
	if e.ownerProfile == nil || !e.ownerProfile.Enabled() {
		return fmt.Errorf("owner profile: set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	return e.ownerProfile.Write(content, mode)
}

// kbRoot builds a folderread.Root from the current knowledge config so
// dashboard folder changes take effect without a restart.
func (e *dispatchExecutor) kbRoot(ctx context.Context) *folderread.Root {
	cfg, _ := knowledge.LoadConfig(ctx, e.store)
	return folderread.New(cfg.FolderPath)
}

// vaultReader builds an obsidian.Reader over the folder the agent reads notes
// from — see vaultReadPath. So read_note/backlinks/search_notes cover both the
// owner's dropped notes and the agent's generated ones.
func (e *dispatchExecutor) vaultReader(ctx context.Context) *obsidian.Reader {
	return obsidian.NewReader(vaultReadPath(ctx, e.store))
}

// ensureDir creates dir (and parents) if missing, logging but not failing.
func ensureDir(dir string) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("vault: mkdir %s: %v", dir, err)
	}
}

// defaultKnowledgeFolder is where `make run` auto-creates the knowledge vault on
// first run. Kept separate so it's testable / overridable.
func defaultKnowledgeFolder() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Documents", "Claude Bridge Knowledge"), nil
}

// ensureKnowledgeFolder makes the dispatch knowledge tools work out of the box.
// If no KB folder is configured, it creates folder (a Vault subfolder for
// generated notes, a .obsidian marker so Obsidian opens it as a vault, and a
// starter note) and saves the path to config so it shows up on the Knowledge
// tab. If a folder is already set, it is left untouched — never override the
// owner's choice. Returns the (possibly updated) config.
func ensureKnowledgeFolder(ctx context.Context, st *store.Store, cfg knowledge.Config, folder string) knowledge.Config {
	if strings.TrimSpace(cfg.FolderPath) != "" {
		return cfg
	}
	ensureDir(folder)
	ensureDir(filepath.Join(folder, "Vault"))
	ensureDir(filepath.Join(folder, ".obsidian")) // marks folder as an Obsidian vault
	seedWelcomeNote(folder)
	cfg.FolderPath = folder
	if err := knowledge.SaveConfig(ctx, st, cfg); err != nil {
		log.Printf("[knowledge] save default folder config: %v", err)
		return cfg
	}
	log.Printf("[knowledge] auto-created knowledge vault at %s", folder)
	return cfg
}

// seedWelcomeNote writes a starter note iff absent (never clobber owner edits).
func seedWelcomeNote(folder string) {
	p := filepath.Join(folder, "Welcome.md")
	if _, err := os.Stat(p); err == nil {
		return
	}
	const body = `# Welcome to your Claude Bridge knowledge vault

Drop policy docs, brochures, and notes anywhere in this folder. Claude
classifies and indexes them, and the dispatch agent can read them.

Agent-generated notes (client profiles, topic summaries) land in **Vault/**.
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		log.Printf("[knowledge] seed welcome note: %v", err)
	}
}

// seedProfileNote writes a starter Profile.md iff absent and a vault exists, so
// the owner has a note to edit in Obsidian. Never clobbers an existing profile.
func seedProfileNote(vaultDir string) {
	if strings.TrimSpace(vaultDir) == "" {
		return
	}
	p := filepath.Join(vaultDir, ownerprofile.ProfileFilename)
	if _, err := os.Stat(p); err == nil {
		return
	}
	const body = `# Owner Profile

This note is the dispatch bot's persona and the shared profile across Cowork and
Claude Bridge. Edit it here, in the Cowork app (it calls update_owner_profile),
or by telling the Telegram bot "update my profile ...".

## Who I am

(describe yourself, your business, your clients)

## How the assistant should behave

(tone, language, do's and don'ts)
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		log.Printf("[ownerprofile] seed profile note: %v", err)
	}
}

// vaultReadPath is the folder the agent reads notes FROM: the explicit Obsidian
// vault path if the owner set one, otherwise the whole knowledge-base folder
// (so notes the owner dropped anywhere in it are readable). May be "".
func vaultReadPath(ctx context.Context, st *store.Store) string {
	acfg, _ := agent.LoadConfig(ctx, st)
	if p := strings.TrimSpace(acfg.ObsidianVaultPath); p != "" {
		ensureDir(p)
		return p
	}
	kcfg, _ := knowledge.LoadConfig(ctx, st)
	return strings.TrimSpace(kcfg.FolderPath)
}

// vaultWritePath is where the agent WRITES generated notes (Clients/Topics/
// Cowork): the explicit vault path if set, else a "Vault" subfolder
// auto-created inside the knowledge-base folder. Keeping generated files in one
// subfolder keeps the owner's knowledge folder tidy while staying indexed
// (the watcher scans the KB folder recursively). Returns "" if no KB folder.
func vaultWritePath(ctx context.Context, st *store.Store) string {
	acfg, _ := agent.LoadConfig(ctx, st)
	if p := strings.TrimSpace(acfg.ObsidianVaultPath); p != "" {
		ensureDir(p)
		return p
	}
	kcfg, _ := knowledge.LoadConfig(ctx, st)
	kb := strings.TrimSpace(kcfg.FolderPath)
	if kb == "" {
		return ""
	}
	vault := filepath.Join(kb, "Vault")
	ensureDir(vault)
	return vault
}

func (e *dispatchExecutor) ListKB(ctx context.Context, subdir string) ([]agent.KBEntry, error) {
	r := e.kbRoot(ctx)
	if !r.Enabled() {
		return nil, fmt.Errorf("set a knowledge base folder on the Knowledge tab first")
	}
	rows, err := r.List(subdir)
	if err != nil {
		return nil, err
	}
	out := make([]agent.KBEntry, 0, len(rows))
	for _, x := range rows {
		out = append(out, agent.KBEntry{Name: x.Name, RelPath: x.RelPath, Size: x.Size, IsDir: x.IsDir, IsText: x.IsText})
	}
	return out, nil
}

func (e *dispatchExecutor) ReadKB(ctx context.Context, path string) (string, *agent.KBEntry, error) {
	r := e.kbRoot(ctx)
	if !r.Enabled() {
		return "", nil, fmt.Errorf("set a knowledge base folder on the Knowledge tab first")
	}
	text, ent, err := r.Read(path)
	if err != nil {
		return "", nil, err
	}
	var ve *agent.KBEntry
	if ent != nil {
		ve = &agent.KBEntry{Name: ent.Name, RelPath: ent.RelPath, Size: ent.Size, IsText: ent.IsText}
	}
	return text, ve, nil
}

func (e *dispatchExecutor) ReadNote(ctx context.Context, name string) (*agent.NoteView, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	n, err := rd.ReadNote(name)
	if err != nil {
		return nil, err
	}
	return &agent.NoteView{
		Name: n.Name, RelPath: n.RelPath, Frontmatter: n.Frontmatter,
		Body: n.Body, OutLinks: n.OutLinks, Tags: n.Tags,
	}, nil
}

func (e *dispatchExecutor) Backlinks(ctx context.Context, name string) ([]string, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	return rd.Backlinks(name)
}

func (e *dispatchExecutor) SearchNotes(ctx context.Context, query, tag string) ([]agent.NoteHit, error) {
	rd := e.vaultReader(ctx)
	if !rd.Enabled() {
		return nil, fmt.Errorf("set a knowledge-base folder (or Obsidian vault) on the Knowledge tab first")
	}
	hits, err := rd.Search(query, tag)
	if err != nil {
		return nil, err
	}
	out := make([]agent.NoteHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, agent.NoteHit{Note: h.Note, Line: h.Line, Snippet: h.Snippet})
	}
	return out, nil
}

// bootTelegram constructs and starts a Telegram client from saved agent
// config. Returns nil if no token is configured. The handler routes inbound
// owner messages through the dispatcher.
func bootTelegram(ctx context.Context, appStore *store.Store, dispatcher *agent.Dispatcher, cl *claude.Client) *telegram.Client {
	cfg, err := agent.LoadConfig(ctx, appStore)
	if err != nil {
		log.Printf("telegram: LoadConfig failed: %v", err)
		return nil
	}
	if cfg.TelegramBotToken == "" {
		return nil
	}
	if len(cfg.OwnerTelegramIDs) == 0 {
		log.Printf("telegram: bot token set but no owner_telegram_ids — bot will drop all messages")
	}
	c := telegram.New(cfg.TelegramBotToken, cfg.OwnerTelegramIDs)
	c.SetHandler(func(ctx context.Context, m *telegram.Message) string {
		if dispatcher == nil {
			return "Got: " + m.Text
		}
		// Show "typing..." while the dispatcher runs (Claude calls take 3-15s
		// typically). Telegram clears the status after 5s, so refresh every 4s
		// until the handler returns or ctx ends.
		stopTyping := make(chan struct{})
		go func() {
			_ = c.SendChatAction(ctx, m.Chat.ID, "typing")
			tick := time.NewTicker(4 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-stopTyping:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					_ = c.SendChatAction(ctx, m.Chat.ID, "typing")
				}
			}
		}()
		defer close(stopTyping)

		// Convert any attachment (PDF/photo/doc) into plain text the
		// dispatcher can treat like a typed message. If extraction yields a
		// direct reply (unsupported type, oversized file), short-circuit.
		text, ok, err := intake.ExtractMessage(ctx, c, cl, m)
		if err != nil {
			log.Printf("telegram: intake failed: %v", err)
			msg := err.Error()
			if len(msg) > 120 {
				msg = msg[:120] + "..."
			}
			return "Couldn't read that attachment — " + msg
		}
		if !ok {
			return text
		}

		res := dispatcher.Run(ctx, agent.DispatchInput{
			Channel: "telegram",
			OwnerID: fmt.Sprintf("%d", m.From.ID),
			Message: text,
		})
		return res.UserReply
	})
	go func() {
		if err := c.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("telegram: stopped: %v", err)
		}
	}()
	return c
}

// version is embedded at build time via -ldflags "-X main.version=<git-sha>".
var version = "dev"

const (
	defaultPort    = 10002
	defaultTLSPort = 10003
)

func main() {
	mcpMode := flag.Bool("mcp", false, "Run as MCP server for Claude Desktop (stdio JSON-RPC)")
	port := flag.Int("port", defaultPort, "HTTP server port")
	tlsPort := flag.Int("tls-port", defaultTLSPort, "HTTPS server port (for Claude Desktop connector)")
	noTray := flag.Bool("no-tray", false, "Run without system tray (headless mode)")
	dataDir := flag.String("data-dir", "", "Directory for session data (default: ~/.claude-bridge)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	pprofAddr := flag.String("pprof", "", "If set (e.g. localhost:6060), serve net/http/pprof for CPU/goroutine profiling")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Optional profiling server (localhost only). Lets you capture a CPU profile
	// or a full goroutine dump while the app misbehaves — see docs/DEBUGGING-CPU.md.
	if *pprofAddr != "" {
		go func() {
			log.Printf("[pprof] profiling server on http://%s/debug/pprof/", *pprofAddr)
			if err := http.ListenAndServe(*pprofAddr, nil); err != nil {
				log.Printf("[pprof] server stopped: %v", err)
			}
		}()
	}

	// Resolve data directory.
	dd := *dataDir
	if dd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Cannot determine home directory: %v", err)
		}
		dd = filepath.Join(home, ".claude-bridge")
	}

	// MCP stdio mode: run the stdio MCP server and exit.
	// This is a fallback for Claude Code or manual testing.
	if *mcpMode {
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
		mcpServer := mcp.NewStdioServer(baseURL)
		if err := mcpServer.Run(); err != nil {
			log.Fatalf("MCP server error: %v", err)
		}
		return
	}

	// Open app-level SQLite store.
	appStore, err := store.New(dd)
	if err != nil {
		log.Fatalf("Failed to open app store: %v", err)
	}
	defer appStore.Close()

	// Normal mode: start HTTP + HTTPS servers + optional system tray.
	wa := whatsapp.NewManager(filepath.Join(dd, "whatsapp"), appStore)

	// Browser engine for all browser-based connectors (Facebook, Instagram, etc).
	browserEngine := browser.NewEngine(filepath.Join(dd, "browser"), true) // headless by default
	browserEngine.EnsureBrowser()                                          // start checking/downloading Chrome in background
	fb := facebook.New(browserEngine, appStore)

	// Boot reconnects previously connected WhatsApp accounts.
	if err := wa.Boot(); err != nil {
		log.Printf("WARNING: WhatsApp boot failed: %v", err)
	}

	// Boot loads saved Facebook Messenger OAuth config and starts polling if connected.
	fb.Boot(context.Background())

	// Boot the knowledge subsystem: load config + API key, start pipeline + watcher.
	knowCtx := context.Background()
	knowCfg, _ := knowledge.LoadConfig(knowCtx, appStore)
	if folder, err := defaultKnowledgeFolder(); err != nil {
		log.Printf("[knowledge] cannot resolve default folder: %v", err)
	} else {
		knowCfg = ensureKnowledgeFolder(knowCtx, appStore, knowCfg, folder)
	}
	knowClient := claude.New("", knowCfg.Model)
	knowPipeline := knowledge.NewPipeline(appStore, knowClient)
	knowPipeline.Configure(knowCfg)

	// Attach Ollama embedder for vector search (optional — silently skipped if Ollama not running).
	knowEmbedder := knowledge.NewEmbedder(knowCfg.OllamaURL, knowCfg.EmbedModel)
	if knowEmbedder.Available(knowCtx) {
		knowPipeline.SetEmbedder(knowEmbedder)
		log.Printf("[knowledge] Ollama available at %s — vector search enabled", knowEmbedder.BaseURL())
	} else {
		log.Printf("[knowledge] Ollama not available — using FTS5 keyword search only")
		knowEmbedder = nil
	}

	knowPipeline.Start()
	knowWatcher := knowledge.NewWatcher(appStore, knowPipeline)
	if knowCfg.FolderPath != "" {
		if err := knowWatcher.SetFolder(knowCfg.FolderPath); err != nil {
			log.Printf("WARNING: knowledge watcher failed to start: %v", err)
		}
	}

	// Boot the auto-reply agent subsystem.
	agentReplier := agent.NewReplier(knowClient, appStore)
	if knowEmbedder != nil {
		agentReplier.SetEmbedder(knowEmbedder)
	}
	agentRunner := agent.NewRunner(agentReplier, wa.SendMessage, appStore)
	agentRunner.Start()
	wa.SetAgentCallback(agentRunner.Enqueue)

	srv := server.New(wa, fb, appStore, browserEngine, *port)
	srv.SetKnowledge(knowClient, knowPipeline, knowWatcher)
	srv.SetEmbedder(knowEmbedder)
	srv.SetAgent(agentRunner)

	// Build the dispatcher (Claude + executor + audit log). Wired into both
	// the WhatsApp Runner (for owner-JID messages) and the Telegram connector
	// (for any allowlisted user). Obsidian writer is constructed from saved
	// config; an empty path makes it a silent no-op.
	extractor := profile.NewExtractor(knowClient)
	vaultPath := vaultWritePath(context.Background(), appStore)
	obsidianWriter := obsidian.New(vaultPath)
	coworkRoot := cowork.New(vaultPath)
	ownerProfileStore := ownerprofile.New(vaultPath)
	seedProfileNote(vaultPath)
	// Dispatcher runs on sonnet for better action selection + replies. The
	// shared knowClient stays on haiku for bulk classification/auto-reply.
	dispatchClient := claude.New("", "claude-sonnet-4-6")
	dispatcher := agent.NewDispatcher(
		dispatchClient,
		&dispatchExecutor{
			wa:           wa,
			bq:           srv.BatchQueue(),
			store:        appStore,
			extractor:    extractor,
			obsidian:     obsidianWriter,
			cowork:       coworkRoot,
			ownerProfile: ownerProfileStore,
		},
		&dispatchStoreAdapter{s: appStore},
	)
	agentRunner.SetDispatcher(dispatcher)

	// Background session compactor: folds idle conversations' turns into their
	// rolling summary (owner offline). Uses haiku (knowClient) — cheap; summary
	// quality is secondary. The "cron" the owner asked for, in-process.
	compactor := &agent.Compactor{
		Store:      &dispatchStoreAdapter{s: appStore},
		Summarizer: knowClient,
		Logger:     log.Default(),
	}
	compactor.Start(context.Background())

	// Index the Cowork output folder into the knowledge base so routine
	// outputs (md/pdf/images dropped by routines or the owner) become
	// search_kb-visible. Best-effort — empty vault is a silent no-op. We
	// ensure today's folder exists so the watcher has a directory to add;
	// new date folders are picked up on the next Rescan.
	srv.SetCowork(coworkRoot)
	srv.SetOwnerProfile(ownerProfileStore)
	srv.SetDispatchProbe(func(ctx context.Context, channel, ownerID, message string) server.DispatchProbeReply {
		res := dispatcher.Run(ctx, agent.DispatchInput{
			Channel: channel,
			OwnerID: ownerID,
			Message: message,
		})
		return server.DispatchProbeReply{
			Action:    string(res.Action),
			UserReply: res.UserReply,
			Error:     res.Error,
		}
	})
	if coworkRoot.Enabled() {
		if _, err := coworkRoot.EnsureToday(); err != nil {
			log.Printf("[cowork] ensure today folder: %v", err)
		}
		if err := knowWatcher.SetExtraRoots(coworkRoot.Path); err != nil {
			log.Printf("[cowork] register KB extra root: %v", err)
		}
	}

	// Telegram connector with live-reload support. The save handler on the
	// dashboard calls srv.tgReloader after persisting new TG fields — that
	// invokes restartTelegram below, which stops the running long-poll (if
	// any) and starts a fresh one with the new token + allowlist. No app
	// restart required.
	var (
		tgMu      sync.Mutex
		tgCurrent *telegram.Client
	)
	restartTelegram := func() error {
		tgMu.Lock()
		defer tgMu.Unlock()

		if tgCurrent != nil {
			tgCurrent.Stop()
			tgCurrent = nil
			srv.SetTelegram(nil)
			// Give Telegram's edge a moment to release the prior long-poll
			// connection so a new getUpdates doesn't trip Conflict 409.
			time.Sleep(300 * time.Millisecond)
		}
		c := bootTelegram(context.Background(), appStore, dispatcher, dispatchClient)
		if c != nil {
			srv.SetTelegram(c)
			tgCurrent = c
		}
		return nil
	}
	srv.SetTelegramReloader(restartTelegram)

	// Initial boot from saved config.
	if c := bootTelegram(context.Background(), appStore, dispatcher, dispatchClient); c != nil {
		tgCurrent = c
		srv.SetTelegram(c)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

	// Start self-updater: checks GitHub Releases every 6h, signals dashboard when ready.
	upd := updater.New(version, func() { srv.SetUpdateReady() })
	upd.Start()

	// Start HTTPS server for Claude Desktop (requires https:// URL).
	if err := srv.StartTLS(*tlsPort, dd); err != nil {
		log.Printf("WARNING: HTTPS server failed to start: %v", err)
		log.Printf("  Claude Desktop connector will not work without HTTPS.")
		log.Printf("  The HTTP dashboard at http://127.0.0.1:%d still works.", *port)
	}

	log.Printf("─────────────────────────────────────────────────")
	log.Printf("  Dashboard:  http://127.0.0.1:%d", *port)
	log.Printf("  Claude URL: https://127.0.0.1:%d/mcp/sse", *tlsPort)
	log.Printf("  Data dir:   %s", dd)
	log.Printf("─────────────────────────────────────────────────")

	if *noTray {
		// Headless mode: wait for interrupt signal.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		wa.DisconnectAll()
		browserEngine.Stop()
		knowWatcher.Stop()
		knowPipeline.Stop()
		compactor.Stop()
		srv.Stop()
	} else {
		// System tray mode: blocks until user quits via tray.
		tray.Run(*port, func() {
			log.Println("Shutting down via tray...")
			wa.DisconnectAll()
			browserEngine.Stop()
			knowWatcher.Stop()
			knowPipeline.Stop()
			compactor.Stop()
			srv.Stop()
		})
	}
}
