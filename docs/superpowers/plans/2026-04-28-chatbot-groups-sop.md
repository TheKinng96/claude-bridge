# WhatsApp Chatbot — Contact Groups, SOP Modes & Review UI — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add contact group management, per-group reply SOP (auto/review/off), a pending-reply review UI, and magic-link remote auth to the existing WhatsApp auto-reply agent.

**Architecture:** Groups are first-class DB entities (manual or auto-assigned). Effective reply mode resolves per-contact at message time (manual group > auto group > global). Pending replies are stored in SQLite and approved/rejected via a new Messages dashboard tab. Auth uses single-use WhatsApp magic links (no password).

**Tech Stack:** Go, SQLite (modernc.org/sqlite), net/http, `crypto/rand` + `crypto/sha256` for tokens.

**Spec:** `docs/superpowers/specs/2026-04-28-chatbot-design.md`

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `internal/store/store.go` | Modify | Add migrations + 5 new types + CRUD methods |
| `internal/agent/config.go` | Modify | Add `GlobalReplyMode`, `OwnerJID`, `AutoSyncFrequency` to `Config` |
| `internal/agent/resolver.go` | Create | `ResolveReplyMode()` — manual group > auto group > global |
| `internal/agent/runner.go` | Modify | Use resolver; branch auto/review/off; detect `!login`; upsert contacts |
| `internal/server/auth.go` | Create | Session store, `/auth`, `/login`, `sessionMiddleware` |
| `internal/server/contacts.go` | Create | API handlers: contacts + groups CRUD |
| `internal/server/messages.go` | Create | API handlers: pending replies list/approve/reject/edit-send |
| `internal/server/html_contacts.go` | Create | Contacts tab HTML constant |
| `internal/server/html_messages.go` | Create | Messages tab HTML constant |
| `internal/server/server.go` | Modify | Register new routes, apply session middleware, add nav entries |
| `internal/server/html_shared.go` | Modify | Add Contacts + Messages to nav |

---

## Task 1: DB Migrations — New Tables

**Files:**
- Modify: `internal/store/store.go` (the `migrate()` function, ~line 168)

- [ ] **Step 1: Add 5 new CREATE TABLE migrations**

In `migrate()`, append these strings to the `migrations []string` slice (after the existing entries, before the `for _, m := range migrations` loop):

```go
`CREATE TABLE IF NOT EXISTS contacts (
  id            INTEGER PRIMARY KEY,
  jid           TEXT NOT NULL UNIQUE,
  platform      TEXT NOT NULL DEFAULT 'whatsapp',
  push_name     TEXT NOT NULL DEFAULT '',
  first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,

`CREATE TABLE IF NOT EXISTS groups (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  type       TEXT NOT NULL CHECK(type IN ('manual','auto')),
  reply_mode TEXT NOT NULL CHECK(reply_mode IN ('auto','review','off')),
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`,

`CREATE TABLE IF NOT EXISTS contact_groups (
  contact_id  INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  group_id    INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  source      TEXT NOT NULL CHECK(source IN ('manual','auto')),
  assigned_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (contact_id, group_id)
)`,

`CREATE TABLE IF NOT EXISTS pending_replies (
  id             INTEGER PRIMARY KEY,
  contact_jid    TEXT NOT NULL,
  account_jid    TEXT NOT NULL,
  platform       TEXT NOT NULL DEFAULT 'whatsapp',
  incoming_msg   TEXT NOT NULL,
  proposed_reply TEXT NOT NULL,
  status         TEXT NOT NULL DEFAULT 'pending'
                 CHECK(status IN ('pending','approved','rejected','sent')),
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  reviewed_at    DATETIME
)`,

`CREATE TABLE IF NOT EXISTS magic_tokens (
  id         INTEGER PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at DATETIME NOT NULL,
  used_at    DATETIME
)`,
```

- [ ] **Step 2: Build to verify migrations compile**

```bash
go build ./...
```

Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/store/store.go
git commit -m "store: add migrations for contacts, groups, pending_replies, magic_tokens"
```

---

## Task 2: Store — Contact + Group Types and CRUD

**Files:**
- Modify: `internal/store/store.go` (append after existing type/method definitions)

- [ ] **Step 1: Add type definitions**

Add these types near the other type definitions at the top of `store.go`:

```go
type Contact struct {
    ID          int64     `json:"id"`
    JID         string    `json:"jid"`
    Platform    string    `json:"platform"`
    PushName    string    `json:"push_name"`
    FirstSeenAt time.Time `json:"first_seen_at"`
}

type Group struct {
    ID        int64     `json:"id"`
    Name      string    `json:"name"`
    Type      string    `json:"type"`
    ReplyMode string    `json:"reply_mode"`
    CreatedAt time.Time `json:"created_at"`
}
```

- [ ] **Step 2: Add contact CRUD methods**

Append to `store.go`:

```go
func (s *Store) UpsertContact(ctx context.Context, jid, platform, pushName string) (*Contact, error) {
    _, err := s.db.ExecContext(ctx,
        `INSERT INTO contacts (jid, platform, push_name) VALUES (?, ?, ?)
         ON CONFLICT(jid) DO UPDATE SET push_name=excluded.push_name`,
        jid, platform, pushName)
    if err != nil {
        return nil, err
    }
    return s.GetContact(ctx, jid)
}

func (s *Store) GetContact(ctx context.Context, jid string) (*Contact, error) {
    row := s.db.QueryRowContext(ctx,
        `SELECT id, jid, platform, push_name, first_seen_at FROM contacts WHERE jid=?`, jid)
    var c Contact
    if err := row.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
        if err == sql.ErrNoRows {
            return nil, nil
        }
        return nil, err
    }
    return &c, nil
}

func (s *Store) ListContacts(ctx context.Context) ([]Contact, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT id, jid, platform, push_name, first_seen_at FROM contacts ORDER BY first_seen_at DESC`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []Contact
    for rows.Next() {
        var c Contact
        if err := rows.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
            return nil, err
        }
        out = append(out, c)
    }
    return out, rows.Err()
}
```

- [ ] **Step 3: Add group CRUD methods**

Append to `store.go`:

```go
func (s *Store) CreateGroup(ctx context.Context, name, groupType, replyMode string) (*Group, error) {
    res, err := s.db.ExecContext(ctx,
        `INSERT INTO groups (name, type, reply_mode) VALUES (?, ?, ?)`,
        name, groupType, replyMode)
    if err != nil {
        return nil, err
    }
    id, _ := res.LastInsertId()
    return s.GetGroup(ctx, id)
}

func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
    row := s.db.QueryRowContext(ctx,
        `SELECT id, name, type, reply_mode, created_at FROM groups WHERE id=?`, id)
    var g Group
    if err := row.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
        if err == sql.ErrNoRows {
            return nil, nil
        }
        return nil, err
    }
    return &g, nil
}

func (s *Store) UpdateGroup(ctx context.Context, id int64, name, replyMode string) error {
    _, err := s.db.ExecContext(ctx,
        `UPDATE groups SET name=?, reply_mode=? WHERE id=?`, name, replyMode, id)
    return err
}

func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
    _, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE id=?`, id)
    return err
}

func (s *Store) ListGroups(ctx context.Context) ([]Group, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT id, name, type, reply_mode, created_at FROM groups ORDER BY type DESC, name ASC`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []Group
    for rows.Next() {
        var g Group
        if err := rows.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
            return nil, err
        }
        out = append(out, g)
    }
    return out, rows.Err()
}

func (s *Store) AssignContactToGroup(ctx context.Context, contactID, groupID int64, source string) error {
    _, err := s.db.ExecContext(ctx,
        `INSERT OR REPLACE INTO contact_groups (contact_id, group_id, source) VALUES (?, ?, ?)`,
        contactID, groupID, source)
    return err
}

func (s *Store) RemoveContactFromGroup(ctx context.Context, contactID, groupID int64) error {
    _, err := s.db.ExecContext(ctx,
        `DELETE FROM contact_groups WHERE contact_id=? AND group_id=?`, contactID, groupID)
    return err
}

// GetContactGroups returns groups for a contact, manual groups first.
func (s *Store) GetContactGroups(ctx context.Context, contactID int64) ([]Group, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT g.id, g.name, g.type, g.reply_mode, g.created_at
         FROM groups g
         JOIN contact_groups cg ON cg.group_id = g.id
         WHERE cg.contact_id = ?
         ORDER BY CASE cg.source WHEN 'manual' THEN 0 ELSE 1 END, g.name ASC`,
        contactID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []Group
    for rows.Next() {
        var g Group
        if err := rows.Scan(&g.ID, &g.Name, &g.Type, &g.ReplyMode, &g.CreatedAt); err != nil {
            return nil, err
        }
        out = append(out, g)
    }
    return out, rows.Err()
}

func (s *Store) ListContactsWithGroups(ctx context.Context) ([]Contact, map[int64][]Group, error) {
    contacts, err := s.ListContacts(ctx)
    if err != nil {
        return nil, nil, err
    }
    groupsByContact := make(map[int64][]Group)
    for _, c := range contacts {
        groups, err := s.GetContactGroups(ctx, c.ID)
        if err != nil {
            return nil, nil, err
        }
        groupsByContact[c.ID] = groups
    }
    return contacts, groupsByContact, nil
}

func (s *Store) ListGroupContacts(ctx context.Context, groupID int64) ([]Contact, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT c.id, c.jid, c.platform, c.push_name, c.first_seen_at
         FROM contacts c
         JOIN contact_groups cg ON cg.contact_id = c.id
         WHERE cg.group_id = ?
         ORDER BY c.push_name ASC`,
        groupID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []Contact
    for rows.Next() {
        var c Contact
        if err := rows.Scan(&c.ID, &c.JID, &c.Platform, &c.PushName, &c.FirstSeenAt); err != nil {
            return nil, err
        }
        out = append(out, c)
    }
    return out, rows.Err()
}
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go
git commit -m "store: add Contact + Group types and CRUD"
```

---

## Task 3: Store — PendingReply + MagicToken CRUD

**Files:**
- Modify: `internal/store/store.go`

- [ ] **Step 1: Add PendingReply type and CRUD**

Add the type near the other types:

```go
type PendingReply struct {
    ID            int64      `json:"id"`
    ContactJID    string     `json:"contact_jid"`
    AccountJID    string     `json:"account_jid"`
    Platform      string     `json:"platform"`
    IncomingMsg   string     `json:"incoming_msg"`
    ProposedReply string     `json:"proposed_reply"`
    Status        string     `json:"status"`
    CreatedAt     time.Time  `json:"created_at"`
    ReviewedAt    *time.Time `json:"reviewed_at,omitempty"`
}
```

Append the CRUD methods:

```go
func (s *Store) CreatePendingReply(ctx context.Context, contactJID, accountJID, platform, incomingMsg, proposedReply string) (int64, error) {
    res, err := s.db.ExecContext(ctx,
        `INSERT INTO pending_replies (contact_jid, account_jid, platform, incoming_msg, proposed_reply)
         VALUES (?, ?, ?, ?, ?)`,
        contactJID, accountJID, platform, incomingMsg, proposedReply)
    if err != nil {
        return 0, err
    }
    return res.LastInsertId()
}

func (s *Store) ListPendingReplies(ctx context.Context, status string) ([]PendingReply, error) {
    var rows *sql.Rows
    var err error
    if status == "" {
        rows, err = s.db.QueryContext(ctx,
            `SELECT id, contact_jid, account_jid, platform, incoming_msg, proposed_reply, status, created_at, reviewed_at
             FROM pending_replies ORDER BY created_at DESC`)
    } else {
        rows, err = s.db.QueryContext(ctx,
            `SELECT id, contact_jid, account_jid, platform, incoming_msg, proposed_reply, status, created_at, reviewed_at
             FROM pending_replies WHERE status=? ORDER BY created_at DESC`, status)
    }
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    var out []PendingReply
    for rows.Next() {
        var p PendingReply
        var reviewedAt sql.NullTime
        if err := rows.Scan(&p.ID, &p.ContactJID, &p.AccountJID, &p.Platform,
            &p.IncomingMsg, &p.ProposedReply, &p.Status, &p.CreatedAt, &reviewedAt); err != nil {
            return nil, err
        }
        if reviewedAt.Valid {
            p.ReviewedAt = &reviewedAt.Time
        }
        out = append(out, p)
    }
    return out, rows.Err()
}

func (s *Store) GetPendingReply(ctx context.Context, id int64) (*PendingReply, error) {
    row := s.db.QueryRowContext(ctx,
        `SELECT id, contact_jid, account_jid, platform, incoming_msg, proposed_reply, status, created_at, reviewed_at
         FROM pending_replies WHERE id=?`, id)
    var p PendingReply
    var reviewedAt sql.NullTime
    if err := row.Scan(&p.ID, &p.ContactJID, &p.AccountJID, &p.Platform,
        &p.IncomingMsg, &p.ProposedReply, &p.Status, &p.CreatedAt, &reviewedAt); err != nil {
        if err == sql.ErrNoRows {
            return nil, nil
        }
        return nil, err
    }
    if reviewedAt.Valid {
        p.ReviewedAt = &reviewedAt.Time
    }
    return &p, nil
}

func (s *Store) UpdatePendingReplyStatus(ctx context.Context, id int64, status string) error {
    _, err := s.db.ExecContext(ctx,
        `UPDATE pending_replies SET status=?, reviewed_at=CURRENT_TIMESTAMP WHERE id=?`, status, id)
    return err
}

func (s *Store) UpdatePendingReplyContent(ctx context.Context, id int64, newReply string) error {
    _, err := s.db.ExecContext(ctx,
        `UPDATE pending_replies SET proposed_reply=? WHERE id=? AND status='pending'`, newReply, id)
    return err
}
```

- [ ] **Step 2: Add MagicToken type and CRUD**

Add the type:

```go
type MagicToken struct {
    ID        int64      `json:"id"`
    TokenHash string     `json:"token_hash"`
    ExpiresAt time.Time  `json:"expires_at"`
    UsedAt    *time.Time `json:"used_at,omitempty"`
}
```

Append the methods:

```go
func (s *Store) CreateMagicToken(ctx context.Context, tokenHash string, expiresAt time.Time) error {
    _, err := s.db.ExecContext(ctx,
        `INSERT INTO magic_tokens (token_hash, expires_at) VALUES (?, ?)`, tokenHash, expiresAt)
    return err
}

// ConsumeMagicToken validates a token (not expired, not used) and marks it used in one transaction.
// Returns true if valid and consumed, false if invalid/expired/already used.
func (s *Store) ConsumeMagicToken(ctx context.Context, tokenHash string) (bool, error) {
    tx, err := s.db.BeginTx(ctx, nil)
    if err != nil {
        return false, err
    }
    defer tx.Rollback()

    var id int64
    var expiresAt time.Time
    var usedAt sql.NullTime
    row := tx.QueryRowContext(ctx,
        `SELECT id, expires_at, used_at FROM magic_tokens WHERE token_hash=?`, tokenHash)
    if err := row.Scan(&id, &expiresAt, &usedAt); err != nil {
        if err == sql.ErrNoRows {
            return false, nil
        }
        return false, err
    }
    if usedAt.Valid || time.Now().After(expiresAt) {
        return false, nil
    }
    if _, err := tx.ExecContext(ctx,
        `UPDATE magic_tokens SET used_at=CURRENT_TIMESTAMP WHERE id=?`, id); err != nil {
        return false, err
    }
    return true, tx.Commit()
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/store/store.go
git commit -m "store: add PendingReply + MagicToken types and CRUD"
```

---

## Task 4: Agent Config — Add GlobalReplyMode, OwnerJID, AutoSyncFrequency

**Files:**
- Modify: `internal/agent/config.go`

- [ ] **Step 1: Extend Config struct**

Replace the `Config` struct with:

```go
type Config struct {
    Enabled           bool       `json:"enabled"`
    SystemPrompt      string     `json:"system_prompt"`
    FlowSteps         []FlowStep `json:"flow_steps"`
    Model             string     `json:"model"`
    GlobalReplyMode   string     `json:"global_reply_mode"`   // "auto"|"review"|"off"
    OwnerJID          string     `json:"owner_jid"`           // sender JID allowed to trigger !login
    AutoSyncFrequency string     `json:"auto_sync_frequency"` // "daily"|"weekly"|"off"
}
```

- [ ] **Step 2: Default GlobalReplyMode in LoadConfig**

In `LoadConfig`, after unmarshalling, add:

```go
if c.GlobalReplyMode == "" {
    c.GlobalReplyMode = "auto"
}
if c.AutoSyncFrequency == "" {
    c.AutoSyncFrequency = "off"
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/agent/config.go
git commit -m "agent: add GlobalReplyMode, OwnerJID, AutoSyncFrequency to Config"
```

---

## Task 5: Agent Resolver — Effective Reply Mode

**Files:**
- Create: `internal/agent/resolver.go`

- [ ] **Step 1: Create resolver.go**

```go
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
	return groups[0].ReplyMode
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/agent/resolver.go
git commit -m "agent: add ResolveReplyMode — manual group > auto group > global"
```

---

## Task 6: Runner — Integrate Resolver + Pending Reply Branch

**Files:**
- Modify: `internal/agent/runner.go`

- [ ] **Step 1: Add notifier state to Runner struct**

Add `sync` import and extend the `Runner` struct:

```go
import (
    "context"
    "log"
    "strings"
    "sync"
    "time"

    "claude-bridge/internal/store"
)

type Runner struct {
    incoming       chan IncomingMsg
    replier        *Replier
    sender         func(phone, text, fromJID string) error
    store          *store.Store
    lastNotifyTime time.Time
    notifyMu       sync.Mutex
}
```

- [ ] **Step 2: Replace process() with new version**

Replace the full `process()` method:

```go
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

    // !login command: generate magic token and send link back.
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
    text := fmt.Sprintf("You have %d pending repl", len(pending))
    if len(pending) == 1 {
        text += "y"
    } else {
        text += "ies"
    }
    text += " waiting. Review: http://127.0.0.1:10002/messages"
    if err := r.sender(ownerPhone, text, accountJID); err != nil {
        log.Printf("[agent] owner notification error: %v", err)
        return
    }
    r.lastNotifyTime = time.Now()
}
```

- [ ] **Step 3: Add fmt import**

Add `"fmt"` to the import block in `runner.go`.

- [ ] **Step 4: Build**

```bash
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/agent/runner.go
git commit -m "agent: integrate reply mode resolver, pending reply branch, owner notification"
```

---

## Task 7: Runner — !login Magic Token Generation

**Files:**
- Modify: `internal/agent/runner.go`

- [ ] **Step 1: Add crypto imports**

Add to the import block: `"crypto/rand"`, `"crypto/sha256"`, `"encoding/hex"`.

- [ ] **Step 2: Add handleLoginCommand method**

Append to `runner.go`:

```go
func (r *Runner) handleLoginCommand(ctx context.Context, cfg Config, msg IncomingMsg) {
    raw := make([]byte, 32)
    if _, err := rand.Read(raw); err != nil {
        log.Printf("[agent] login token generation error: %v", err)
        return
    }
    token := hex.EncodeToString(raw)
    hash := sha256.Sum256([]byte(token))
    tokenHash := hex.EncodeToString(hash[:])
    expiresAt := time.Now().Add(30 * time.Minute)

    if err := r.store.CreateMagicToken(ctx, tokenHash, expiresAt); err != nil {
        log.Printf("[agent] store magic token error: %v", err)
        return
    }

    ownerPhone := strings.Split(cfg.OwnerJID, "@")[0]
    link := fmt.Sprintf("http://127.0.0.1:10002/auth?token=%s", token)
    text := fmt.Sprintf("Dashboard login link (valid 30 min):\n%s", link)
    if err := r.sender(ownerPhone, text, msg.AccountJID); err != nil {
        log.Printf("[agent] send login link error: %v", err)
    }
}
```

- [ ] **Step 3: Build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/agent/runner.go
git commit -m "agent: handle !login command — generate and send magic link via WhatsApp"
```

---

## Task 8: Server — Auth (Session + Magic Link)

**Files:**
- Create: `internal/server/auth.go`

- [ ] **Step 1: Create auth.go**

```go
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "cb_session"
const sessionDuration = 24 * time.Hour

type sessionStore struct {
    mu      sync.Mutex
    tokens  map[string]time.Time // session token → expiry
}

func newSessionStore() *sessionStore {
    return &sessionStore{tokens: make(map[string]time.Time)}
}

func (ss *sessionStore) create() (string, error) {
    raw := make([]byte, 32)
    if _, err := rand.Read(raw); err != nil {
        return "", err
    }
    token := hex.EncodeToString(raw)
    ss.mu.Lock()
    ss.tokens[token] = time.Now().Add(sessionDuration)
    ss.mu.Unlock()
    return token, nil
}

func (ss *sessionStore) valid(token string) bool {
    ss.mu.Lock()
    defer ss.mu.Unlock()
    exp, ok := ss.tokens[token]
    if !ok {
        return false
    }
    if time.Now().After(exp) {
        delete(ss.tokens, token)
        return false
    }
    return true
}
```

- [ ] **Step 2: Add sessions field to Server struct**

In `internal/server/server.go`, add to the `Server` struct:

```go
sessions *sessionStore
```

In the `New()` function, add initialization:

```go
sessions: newSessionStore(),
```

- [ ] **Step 3: Add handleAuth, handleLogin, sessionMiddleware to auth.go**

Append to `auth.go`:

```go
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
    token := r.URL.Query().Get("token")
    if token == "" {
        http.Redirect(w, r, "/login", http.StatusFound)
        return
    }
    hash := sha256.Sum256([]byte(token))
    tokenHash := hex.EncodeToString(hash[:])
    ok, err := s.store.ConsumeMagicToken(r.Context(), tokenHash)
    if err != nil || !ok {
        http.Error(w, "Link expired or invalid. Send !login via WhatsApp to get a new one.", http.StatusUnauthorized)
        return
    }
    sessionToken, err := s.sessions.create()
    if err != nil {
        http.Error(w, "internal error", http.StatusInternalServerError)
        return
    }
    http.SetCookie(w, &http.Cookie{
        Name:     sessionCookieName,
        Value:    sessionToken,
        Path:     "/",
        HttpOnly: true,
        SameSite: http.SameSiteStrictMode,
        Expires:  time.Now().Add(sessionDuration),
    })
    http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Claude Bridge — Login</title>
<style>body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111}
.card{background:#1e1e1e;border:1px solid #333;border-radius:12px;padding:40px;max-width:400px;text-align:center;color:#eee}
h2{margin:0 0 12px;font-size:22px}p{color:#aaa;font-size:14px;line-height:1.6}</style>
</head><body><div class="card">
<h2>Claude Bridge</h2>
<p>Send <code>!login</code> via WhatsApp from your owner number to receive a login link.</p>
</div></body></html>`))
}

func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Always allow: static assets, auth endpoints, MCP, API (used by runner itself)
        path := r.URL.Path
        if path == "/auth" || path == "/login" ||
            len(path) >= 7 && path[:7] == "/static" ||
            len(path) >= 4 && path[:4] == "/mcp" ||
            len(path) >= 4 && path[:4] == "/api" {
            next.ServeHTTP(w, r)
            return
        }
        cookie, err := r.Cookie(sessionCookieName)
        if err != nil || !s.sessions.valid(cookie.Value) {
            http.Redirect(w, r, "/login", http.StatusFound)
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

- [ ] **Step 4: Register auth routes + apply middleware in buildMux()**

In `internal/server/server.go`, in `buildMux()`:

After `mux := http.NewServeMux()` add:
```go
mux.HandleFunc("/auth", s.handleAuth)
mux.HandleFunc("/login", s.handleLogin)
```

At the bottom of `buildMux()`, before `return mux`, wrap the mux:
```go
return mux
```
Change to:
```go
wrapped := s.sessionMiddleware(mux)
// http.ServeMux doesn't implement http.Handler directly for wrapping,
// so return a new mux that routes everything through middleware.
outer := http.NewServeMux()
outer.Handle("/", wrapped)
return outer
```

Wait — `buildMux()` returns `*http.ServeMux` but wrapping requires `http.Handler`. Change the return type of `buildMux()` to `http.Handler`:

```go
func (s *Server) buildMux() http.Handler {
    mux := http.NewServeMux()
    // ... all existing routes ...
    mux.HandleFunc("/auth", s.handleAuth)
    mux.HandleFunc("/login", s.handleLogin)
    return s.sessionMiddleware(mux)
}
```

Also update the two callers of `buildMux()` in `Start()` and `StartTLS()` — they assign it to `mux` which is passed to `http.Serve`. Since `http.Serve` accepts `http.Handler`, the type change is compatible.

- [ ] **Step 5: Build**

```bash
go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/server/auth.go internal/server/server.go
git commit -m "server: add session store, magic link auth, /auth + /login handlers, session middleware"
```

---

## Task 9: Server — Contacts + Groups API Handlers

**Files:**
- Create: `internal/server/contacts.go`

- [ ] **Step 1: Create contacts.go**

```go
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// GET /contacts — page
func (s *Server) handleContactsPage(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(contactsHTML))
}

// GET /api/contacts
func (s *Server) handleListContacts(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    contacts, groupsByContact, err := s.store.ListContactsWithGroups(ctx)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    type item struct {
        ID          int64        `json:"id"`
        JID         string       `json:"jid"`
        PushName    string       `json:"push_name"`
        Platform    string       `json:"platform"`
        FirstSeenAt string       `json:"first_seen_at"`
        Groups      []groupItem  `json:"groups"`
    }
    type groupItem = map[string]any
    var out []item
    for _, c := range contacts {
        groups := groupsByContact[c.ID]
        gItems := make([]groupItem, 0, len(groups))
        for _, g := range groups {
            gItems = append(gItems, groupItem{"id": g.ID, "name": g.Name, "type": g.Type, "reply_mode": g.ReplyMode})
        }
        out = append(out, item{
            ID: c.ID, JID: c.JID, PushName: c.PushName, Platform: c.Platform,
            FirstSeenAt: c.FirstSeenAt.Format("2006-01-02 15:04"),
            Groups: gItems,
        })
    }
    if out == nil {
        out = []item{}
    }
    writeJSON(w, map[string]any{"ok": true, "contacts": out})
}

// GET /api/groups
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    groups, err := s.store.ListGroups(ctx)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    type item struct {
        ID           int64  `json:"id"`
        Name         string `json:"name"`
        Type         string `json:"type"`
        ReplyMode    string `json:"reply_mode"`
        ContactCount int    `json:"contact_count"`
    }
    var out []item
    for _, g := range groups {
        contacts, _ := s.store.ListGroupContacts(ctx, g.ID)
        out = append(out, item{ID: g.ID, Name: g.Name, Type: g.Type, ReplyMode: g.ReplyMode, ContactCount: len(contacts)})
    }
    if out == nil {
        out = []item{}
    }
    writeJSON(w, map[string]any{"ok": true, "groups": out})
}

// POST /api/groups
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
    var body struct {
        Name      string `json:"name"`
        Type      string `json:"type"`
        ReplyMode string `json:"reply_mode"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
        return
    }
    if body.Name == "" || (body.Type != "manual" && body.Type != "auto") ||
        (body.ReplyMode != "auto" && body.ReplyMode != "review" && body.ReplyMode != "off") {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid fields"})
        return
    }
    g, err := s.store.CreateGroup(r.Context(), body.Name, body.Type, body.ReplyMode)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true, "group": g})
}

// PUT /api/groups/{id}
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
    id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    var body struct {
        Name      string `json:"name"`
        ReplyMode string `json:"reply_mode"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
        return
    }
    if err := s.store.UpdateGroup(r.Context(), id, body.Name, body.ReplyMode); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true})
}

// DELETE /api/groups/{id}
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
    id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    if err := s.store.DeleteGroup(r.Context(), id); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true})
}

// POST /api/contacts/{jid}/groups  body: {"group_id": 1}
func (s *Server) handleAssignContactGroup(w http.ResponseWriter, r *http.Request) {
    jid := extractContactJID(r.URL.Path)
    if jid == "" {
        writeJSON(w, map[string]any{"ok": false, "error": "missing jid"})
        return
    }
    var body struct {
        GroupID int64 `json:"group_id"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid JSON"})
        return
    }
    ctx := r.Context()
    contact, err := s.store.GetContact(ctx, jid)
    if err != nil || contact == nil {
        writeJSON(w, map[string]any{"ok": false, "error": "contact not found"})
        return
    }
    if err := s.store.AssignContactToGroup(ctx, contact.ID, body.GroupID, "manual"); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true})
}

// DELETE /api/contacts/{jid}/groups/{group_id}
func (s *Server) handleRemoveContactGroup(w http.ResponseWriter, r *http.Request) {
    parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/contacts/"), "/groups/")
    if len(parts) != 2 {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid path"})
        return
    }
    jid := parts[0]
    groupID, err := strconv.ParseInt(parts[1], 10, 64)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid group_id"})
        return
    }
    ctx := r.Context()
    contact, err := s.store.GetContact(ctx, jid)
    if err != nil || contact == nil {
        writeJSON(w, map[string]any{"ok": false, "error": "contact not found"})
        return
    }
    if err := s.store.RemoveContactFromGroup(ctx, contact.ID, groupID); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true})
}

// GET /api/groups/{id}/contacts
func (s *Server) handleListGroupContacts(w http.ResponseWriter, r *http.Request) {
    id, err := parseIDFromPath(r.URL.Path, "/api/groups/")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    // strip trailing /contacts
    contacts, err := s.store.ListGroupContacts(r.Context(), id)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    if contacts == nil {
        contacts = []store.Contact{}
    }
    writeJSON(w, map[string]any{"ok": true, "contacts": contacts})
}

func parseIDFromPath(path, prefix string) (int64, error) {
    s := strings.TrimPrefix(path, prefix)
    s = strings.Split(s, "/")[0]
    return strconv.ParseInt(s, 10, 64)
}

func extractContactJID(path string) string {
    s := strings.TrimPrefix(path, "/api/contacts/")
    parts := strings.SplitN(s, "/", 2)
    return parts[0]
}
```

Add the missing `store` import at the top of the file:
```go
import (
    "encoding/json"
    "net/http"
    "strconv"
    "strings"

    "claude-bridge/internal/store"
)
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/server/contacts.go
git commit -m "server: add contacts + groups API handlers"
```

---

## Task 10: Server — Pending Replies API Handlers

**Files:**
- Create: `internal/server/messages.go`

- [ ] **Step 1: Create messages.go**

```go
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// GET /messages — page
func (s *Server) handleMessagesPage(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(messagesHTML))
}

// GET /api/pending-replies?status=pending
func (s *Server) handleListPendingReplies(w http.ResponseWriter, r *http.Request) {
    status := r.URL.Query().Get("status")
    replies, err := s.store.ListPendingReplies(r.Context(), status)
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    if replies == nil {
        replies = []store.PendingReply{}
    }
    writeJSON(w, map[string]any{"ok": true, "replies": replies})
}

// POST /api/pending-replies/{id}/approve
func (s *Server) handleApprovePendingReply(w http.ResponseWriter, r *http.Request) {
    id, err := parsePendingReplyID(r.URL.Path, "approve")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    ctx := r.Context()
    p, err := s.store.GetPendingReply(ctx, id)
    if err != nil || p == nil {
        writeJSON(w, map[string]any{"ok": false, "error": "not found"})
        return
    }
    if p.Status != "pending" {
        writeJSON(w, map[string]any{"ok": false, "error": "already reviewed"})
        return
    }
    phone := strings.Split(p.ContactJID, "@")[0]
    if err := s.wa.SendMessage(phone, p.ProposedReply, p.AccountJID); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    _ = s.store.UpdatePendingReplyStatus(ctx, id, "sent")
    now := time.Now()
    _ = s.store.UpsertCachedMessage(ctx, &store.CachedMessage{
        Platform:       p.Platform,
        ConversationID: p.ContactJID,
        MessageID:      "agent-approved-" + strconv.FormatInt(id, 10),
        SenderID:       p.AccountJID,
        SenderName:     "Agent",
        Content:        p.ProposedReply,
        Timestamp:      now,
        IsOutgoing:     true,
    })
    writeJSON(w, map[string]any{"ok": true})
}

// POST /api/pending-replies/{id}/reject
func (s *Server) handleRejectPendingReply(w http.ResponseWriter, r *http.Request) {
    id, err := parsePendingReplyID(r.URL.Path, "reject")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    if err := s.store.UpdatePendingReplyStatus(r.Context(), id, "rejected"); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    writeJSON(w, map[string]any{"ok": true})
}

// POST /api/pending-replies/{id}/edit-send  body: {"reply": "..."}
func (s *Server) handleEditSendPendingReply(w http.ResponseWriter, r *http.Request) {
    id, err := parsePendingReplyID(r.URL.Path, "edit-send")
    if err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid id"})
        return
    }
    var body struct {
        Reply string `json:"reply"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Reply == "" {
        writeJSON(w, map[string]any{"ok": false, "error": "invalid body"})
        return
    }
    ctx := r.Context()
    p, err := s.store.GetPendingReply(ctx, id)
    if err != nil || p == nil {
        writeJSON(w, map[string]any{"ok": false, "error": "not found"})
        return
    }
    if p.Status != "pending" {
        writeJSON(w, map[string]any{"ok": false, "error": "already reviewed"})
        return
    }
    _ = s.store.UpdatePendingReplyContent(ctx, id, body.Reply)
    phone := strings.Split(p.ContactJID, "@")[0]
    if err := s.wa.SendMessage(phone, body.Reply, p.AccountJID); err != nil {
        writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
        return
    }
    _ = s.store.UpdatePendingReplyStatus(ctx, id, "sent")
    now := time.Now()
    _ = s.store.UpsertCachedMessage(ctx, &store.CachedMessage{
        Platform:       p.Platform,
        ConversationID: p.ContactJID,
        MessageID:      "agent-edited-" + strconv.FormatInt(id, 10),
        SenderID:       p.AccountJID,
        SenderName:     "Agent",
        Content:        body.Reply,
        Timestamp:      now,
        IsOutgoing:     true,
    })
    writeJSON(w, map[string]any{"ok": true})
}

func parsePendingReplyID(path, action string) (int64, error) {
    // path: /api/pending-replies/{id}/{action}
    s := strings.TrimPrefix(path, "/api/pending-replies/")
    s = strings.TrimSuffix(s, "/"+action)
    return strconv.ParseInt(s, 10, 64)
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/server/messages.go
git commit -m "server: add pending replies API handlers (list, approve, reject, edit-send)"
```

---

## Task 11: HTML — Messages Tab

**Files:**
- Create: `internal/server/html_messages.go`

- [ ] **Step 1: Create html_messages.go**

```go
package server

const messagesHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Messages</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.tabs{display:flex;gap:2px;margin-bottom:20px;border-bottom:1px solid var(--border)}
.tab{padding:10px 20px;cursor:pointer;font-size:14px;color:var(--text-muted);border-bottom:2px solid transparent;margin-bottom:-1px}
.tab.active{color:var(--accent);border-bottom-color:var(--accent)}
.card{background:var(--bg-card);border:1px solid var(--border);border-radius:12px;padding:20px;margin-bottom:12px;box-shadow:var(--shadow)}
.contact-name{font-weight:600;font-size:15px;color:var(--text)}
.msg-time{font-size:12px;color:var(--text-dim);margin-left:8px}
.msg-label{font-size:12px;color:var(--text-muted);margin:8px 0 4px;text-transform:uppercase;letter-spacing:.5px}
.msg-body{font-size:14px;color:var(--text);background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:10px 12px;line-height:1.5;white-space:pre-wrap}
.msg-body.editable{outline:none}
.actions{display:flex;gap:8px;margin-top:12px;align-items:center}
.btn{padding:7px 16px;border-radius:8px;border:none;cursor:pointer;font-size:13px;font-weight:600}
.btn-approve{background:var(--accent);color:#fff}
.btn-edit{background:var(--bg);border:1px solid var(--border);color:var(--text)}
.btn-reject{background:transparent;border:1px solid #ef4444;color:#ef4444}
.btn-send{background:#10b981;color:#fff}
.bulk-bar{display:flex;gap:10px;align-items:center;margin-bottom:14px}
.empty{text-align:center;color:var(--text-muted);padding:40px;font-size:14px}
input[type=checkbox]{width:16px;height:16px;cursor:pointer}
</style>
</head>
<body>
` + "`" + `${navHTML('messages')}` + "`" + `
<div class="container">
<div class="page-header"><h1>Messages</h1></div>
<div class="tabs">
  <div class="tab active" id="tabPending" onclick="switchTab('pending')">Pending</div>
  <div class="tab" id="tabSent" onclick="switchTab('sent')">Sent</div>
</div>
<div id="bulkBar" class="bulk-bar" style="display:none">
  <input type="checkbox" id="selectAll" onchange="toggleSelectAll(this.checked)">
  <label for="selectAll" style="font-size:13px;color:var(--text)">Select all</label>
  <button class="btn btn-approve" onclick="bulkAction('approve')">Approve Selected</button>
  <button class="btn btn-reject" onclick="bulkAction('reject')">Reject Selected</button>
</div>
<div id="replyList"></div>
</div>
<script>
let currentTab = 'pending';
let replies = [];

async function load() {
  const status = currentTab === 'pending' ? 'pending' : '';
  const r = await fetch('/api/pending-replies?status=' + status);
  const j = await r.json();
  replies = (j.replies || []).filter(x => currentTab === 'pending' ? x.status === 'pending' : x.status !== 'pending');
  render();
}

function switchTab(tab) {
  currentTab = tab;
  document.getElementById('tabPending').classList.toggle('active', tab === 'pending');
  document.getElementById('tabSent').classList.toggle('active', tab === 'sent');
  document.getElementById('bulkBar').style.display = tab === 'pending' ? 'flex' : 'none';
  document.getElementById('selectAll').checked = false;
  load();
}

function render() {
  const list = document.getElementById('replyList');
  if (replies.length === 0) {
    list.innerHTML = '<div class="empty">No ' + currentTab + ' replies.</div>';
    return;
  }
  list.innerHTML = replies.map((p, i) => {
    const name = p.contact_jid.split('@')[0];
    const editing = currentTab === 'pending';
    return ` + "`" + `
    <div class="card" id="card-${p.id}">
      <div style="display:flex;align-items:center;gap:10px">
        ${editing ? '<input type="checkbox" class="row-check" data-id="' + p.id + '">' : ''}
        <span class="contact-name">${name}</span>
        <span class="msg-time">${p.created_at}</span>
        <span style="margin-left:auto;font-size:12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:2px 8px;color:var(--text-muted)">${p.status}</span>
      </div>
      <div class="msg-label">Incoming</div>
      <div class="msg-body">${escHtml(p.incoming_msg)}</div>
      <div class="msg-label">Proposed reply</div>
      <div class="msg-body${editing ? ' editable' : ''}" id="reply-${p.id}" ${editing ? 'contenteditable="true"' : ''}>${escHtml(p.proposed_reply)}</div>
      ${editing ? ` + "`" + `
      <div class="actions">
        <button class="btn btn-approve" onclick="approve(${p.id})">Approve</button>
        <button class="btn btn-send" onclick="editSend(${p.id})">Edit &amp; Send</button>
        <button class="btn btn-reject" onclick="reject(${p.id})">Reject</button>
      </div>` + "`" + ` : ''}
    </div>` + "`";
  }).join('');
}

function escHtml(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

async function approve(id) {
  await fetch('/api/pending-replies/' + id + '/approve', {method:'POST'});
  load();
}

async function reject(id) {
  await fetch('/api/pending-replies/' + id + '/reject', {method:'POST'});
  load();
}

async function editSend(id) {
  const el = document.getElementById('reply-' + id);
  const reply = el ? el.innerText.trim() : '';
  if (!reply) return;
  await fetch('/api/pending-replies/' + id + '/edit-send', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({reply})
  });
  load();
}

function toggleSelectAll(checked) {
  document.querySelectorAll('.row-check').forEach(c => c.checked = checked);
}

async function bulkAction(action) {
  const checked = [...document.querySelectorAll('.row-check:checked')].map(c => parseInt(c.dataset.id));
  if (checked.length === 0) return;
  await Promise.all(checked.map(id => fetch('/api/pending-replies/' + id + '/' + action, {method:'POST'})));
  document.getElementById('selectAll').checked = false;
  load();
}

load();
setInterval(load, 30000);
</script>
</body>
</html>`
```

Note: The `navHTML('messages')` call is a Go template substitution — this HTML is served from a Go handler that injects the nav. Since the current pattern uses Go string constants (not templates), replace the nav call with a static string or use the `navHTML()` Go function in the handler. The simplest approach matching existing code: serve via handler that writes `navHTML("messages") + messagesHTML`.

Revise `html_messages.go` to not use template substitution — instead define:

```go
package server

const messagesHTMLBody = `...` // the full HTML without the nav

func messagesFullHTML() string {
    return `<!DOCTYPE html><html lang="en"><head>
<meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Messages</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>` + messagesCSS + `</head><body>` +
        navHTML("/messages") + messagesHTMLContent + `</body></html>`
}
```

Look at how `html_agent.go` and `html_knowledge.go` handle nav injection — follow that exact pattern. Each page is a `const` string with the full HTML including `navHTML()` call embedded via Go string concatenation in a `func` or directly as a constant using `+`.

Checking `html_knowledge.go` — if it uses a pattern like `headHTML("Knowledge") + navHTML("/setup/knowledge") + bodyContent`, use the same approach:

```go
package server

func messagesPage() string {
    return headHTML("Messages") + `<body>` + navHTML("/messages") + `
<div class="container">` + messagesBodyHTML + `</div>
` + messagesScriptHTML + `</body></html>`
}
```

Then in `messages.go`'s `handleMessagesPage`:
```go
func (s *Server) handleMessagesPage(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Write([]byte(messagesPage()))
}
```

Read `internal/server/html_knowledge.go` lines 1-30 to confirm the exact pattern before writing this file.

- [ ] **Step 2: Read html_knowledge.go to confirm pattern**

```bash
head -30 internal/server/html_knowledge.go
```

Adjust `html_messages.go` to match the exact pattern used there.

- [ ] **Step 3: Build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/server/html_messages.go internal/server/messages.go
git commit -m "server: add Messages tab — pending reply review UI"
```

---

## Task 12: HTML — Contacts Tab

**Files:**
- Create: `internal/server/html_contacts.go`

- [ ] **Step 1: Read html_knowledge.go pattern then create html_contacts.go**

Read `internal/server/html_knowledge.go` lines 1-30 to confirm the `headHTML` + `navHTML` + body pattern, then write `html_contacts.go` following the same structure.

The Contacts page has two views toggled by JS:

**Groups view (default):**
- Table: Name | Type | Contacts | Reply Mode
- Reply Mode shown as a `<select>` (auto/review/off) that POSTs on change
- [+ New Group] button → inline form
- Click row → switch to Contacts view filtered by group

**Contacts view:**
- Search input
- Table: Name | Phone | Groups | First Seen
- Click contact → slide-in panel or expand row showing group checkboxes (manual groups only)
- Groups shown as badges

```go
package server

func contactsPage() string {
    return headHTML("Contacts") + `<body>` + navHTML("/contacts") + `
<div class="container">
<div class="page-header">
  <h1>Contacts</h1>
  <div style="display:flex;gap:8px">
    <button class="btn-secondary" onclick="showView('groups')" id="btnGroups">Groups</button>
    <button class="btn-secondary" onclick="showView('contacts')" id="btnContacts">Contacts</button>
  </div>
</div>

<!-- Groups view -->
<div id="viewGroups">
  <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:14px">
    <div>
      <label style="font-size:13px;color:var(--text-muted);margin-right:8px">Global reply mode:</label>
      <select id="globalMode" onchange="saveGlobalMode(this.value)" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:4px 8px;font-size:13px">
        <option value="auto">Auto</option>
        <option value="review">Review</option>
        <option value="off">Off</option>
      </select>
    </div>
    <button class="btn" onclick="showNewGroupForm()" style="background:var(--accent);color:#fff;border:none;border-radius:8px;padding:8px 16px;cursor:pointer;font-size:13px">+ New Group</button>
  </div>
  <div id="newGroupForm" style="display:none;background:var(--bg-card);border:1px solid var(--border);border-radius:10px;padding:16px;margin-bottom:12px">
    <div style="display:flex;gap:10px;align-items:flex-end">
      <div style="flex:1"><label style="font-size:12px;color:var(--text-muted)">Name</label><br>
        <input id="newGroupName" placeholder="Group name" style="width:100%;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px;box-sizing:border-box;margin-top:4px"></div>
      <div><label style="font-size:12px;color:var(--text-muted)">Type</label><br>
        <select id="newGroupType" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px;margin-top:4px">
          <option value="manual">Manual</option><option value="auto">Auto</option></select></div>
      <div><label style="font-size:12px;color:var(--text-muted)">Reply Mode</label><br>
        <select id="newGroupMode" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px;margin-top:4px">
          <option value="auto">Auto</option><option value="review">Review</option><option value="off">Off</option></select></div>
      <button onclick="createGroup()" style="background:var(--accent);color:#fff;border:none;border-radius:8px;padding:9px 16px;cursor:pointer;font-size:13px">Create</button>
      <button onclick="document.getElementById('newGroupForm').style.display='none'" style="background:transparent;border:1px solid var(--border);color:var(--text);border-radius:8px;padding:9px 12px;cursor:pointer;font-size:13px">Cancel</button>
    </div>
  </div>
  <table style="width:100%;border-collapse:collapse;font-size:14px">
    <thead><tr style="border-bottom:1px solid var(--border)">
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Name</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Type</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Contacts</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Reply Mode</th>
      <th style="padding:10px 8px"></th>
    </tr></thead>
    <tbody id="groupsTable"></tbody>
  </table>
</div>

<!-- Contacts view -->
<div id="viewContacts" style="display:none">
  <input id="contactSearch" placeholder="Search by name or phone..." oninput="renderContacts()"
    style="width:100%;background:var(--bg-card);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:10px 12px;font-size:14px;margin-bottom:14px;box-sizing:border-box">
  <table style="width:100%;border-collapse:collapse;font-size:14px">
    <thead><tr style="border-bottom:1px solid var(--border)">
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Name</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Phone</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Groups</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">First seen</th>
    </tr></thead>
    <tbody id="contactsTable"></tbody>
  </table>
</div>

<!-- Contact detail panel -->
<div id="contactPanel" style="display:none;position:fixed;top:0;right:0;width:340px;height:100vh;background:var(--bg-card);border-left:1px solid var(--border);padding:24px;overflow-y:auto;z-index:100">
  <button onclick="closePanel()" style="float:right;background:transparent;border:none;color:var(--text-muted);font-size:18px;cursor:pointer">✕</button>
  <h3 id="panelName" style="margin:0 0 4px;color:var(--text)"></h3>
  <div id="panelPhone" style="font-size:12px;color:var(--text-dim);margin-bottom:16px"></div>
  <div style="font-size:13px;color:var(--text-muted);margin-bottom:8px;font-weight:500">Manual Groups</div>
  <div id="panelGroups"></div>
</div>
</div>

<script>
let groups = [];
let contacts = [];
let allGroups = [];
let currentContact = null;

async function init() {
  await loadGlobalMode();
  await Promise.all([loadGroups(), loadContacts()]);
}

async function loadGlobalMode() {
  const r = await fetch('/api/agent/config');
  const j = await r.json();
  const sel = document.getElementById('globalMode');
  if (sel) sel.value = j.global_reply_mode || 'auto';
}

async function saveGlobalMode(mode) {
  const r = await fetch('/api/agent/config');
  const j = await r.json();
  await fetch('/api/agent/config', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({...j, global_reply_mode: mode})
  });
}

async function loadGroups() {
  const r = await fetch('/api/groups');
  const j = await r.json();
  groups = j.groups || [];
  allGroups = groups;
  renderGroups();
}

async function loadContacts() {
  const r = await fetch('/api/contacts');
  const j = await r.json();
  contacts = j.contacts || [];
  renderContacts();
}

function renderGroups() {
  const tbody = document.getElementById('groupsTable');
  if (groups.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;padding:40px;color:var(--text-muted)">No groups yet.</td></tr>';
    return;
  }
  tbody.innerHTML = groups.map(g => `
    <tr style="border-bottom:1px solid var(--border)">
      <td style="padding:10px 8px;color:var(--text);font-weight:500">${esc(g.name)}</td>
      <td style="padding:10px 8px"><span style="font-size:11px;background:var(--bg);border:1px solid var(--border);border-radius:4px;padding:2px 6px;color:var(--text-muted)">${g.type}</span></td>
      <td style="padding:10px 8px;color:var(--text-muted)">${g.contact_count}</td>
      <td style="padding:10px 8px">
        <select onchange="updateGroupMode(${g.id}, this.value)" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:4px 8px;font-size:12px">
          <option value="auto" ${g.reply_mode==='auto'?'selected':''}>Auto</option>
          <option value="review" ${g.reply_mode==='review'?'selected':''}>Review</option>
          <option value="off" ${g.reply_mode==='off'?'selected':''}>Off</option>
        </select>
      </td>
      <td style="padding:10px 8px;text-align:right">
        <button onclick="deleteGroup(${g.id})" style="background:transparent;border:none;color:#ef4444;cursor:pointer;font-size:12px">Delete</button>
      </td>
    </tr>`).join('');
}

function renderContacts() {
  const q = (document.getElementById('contactSearch')?.value || '').toLowerCase();
  const filtered = contacts.filter(c =>
    c.push_name.toLowerCase().includes(q) || c.jid.toLowerCase().includes(q));
  const tbody = document.getElementById('contactsTable');
  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;padding:40px;color:var(--text-muted)">No contacts yet.</td></tr>';
    return;
  }
  tbody.innerHTML = filtered.map(c => {
    const phone = c.jid.split('@')[0];
    const badges = (c.groups || []).map(g =>
      `<span style="font-size:11px;background:var(--bg);border:1px solid var(--border);border-radius:4px;padding:2px 6px;color:var(--text-muted);margin-right:4px">${esc(g.name)}</span>`
    ).join('');
    return `<tr style="border-bottom:1px solid var(--border);cursor:pointer" onclick="openContact('${c.jid}')">
      <td style="padding:10px 8px;color:var(--text);font-weight:500">${esc(c.push_name) || '(unknown)'}</td>
      <td style="padding:10px 8px;color:var(--text-muted);font-family:monospace;font-size:13px">${phone}</td>
      <td style="padding:10px 8px">${badges}</td>
      <td style="padding:10px 8px;color:var(--text-dim);font-size:12px">${c.first_seen_at}</td>
    </tr>`;
  }).join('');
}

function showView(view) {
  document.getElementById('viewGroups').style.display = view === 'groups' ? '' : 'none';
  document.getElementById('viewContacts').style.display = view === 'contacts' ? '' : 'none';
  document.getElementById('btnGroups').style.opacity = view === 'groups' ? '1' : '0.5';
  document.getElementById('btnContacts').style.opacity = view === 'contacts' ? '1' : '0.5';
}

function showNewGroupForm() {
  document.getElementById('newGroupForm').style.display = '';
}

async function createGroup() {
  const name = document.getElementById('newGroupName').value.trim();
  const type = document.getElementById('newGroupType').value;
  const reply_mode = document.getElementById('newGroupMode').value;
  if (!name) return;
  await fetch('/api/groups', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name, type, reply_mode})
  });
  document.getElementById('newGroupForm').style.display = 'none';
  document.getElementById('newGroupName').value = '';
  loadGroups();
}

async function updateGroupMode(id, mode) {
  const g = groups.find(x => x.id === id);
  if (!g) return;
  await fetch('/api/groups/' + id, {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: g.name, reply_mode: mode})
  });
  loadGroups();
}

async function deleteGroup(id) {
  if (!confirm('Delete this group?')) return;
  await fetch('/api/groups/' + id, {method: 'DELETE'});
  loadGroups();
}

function openContact(jid) {
  const c = contacts.find(x => x.jid === jid);
  if (!c) return;
  currentContact = c;
  document.getElementById('panelName').textContent = c.push_name || '(unknown)';
  document.getElementById('panelPhone').textContent = c.jid.split('@')[0];
  const manualGroups = allGroups.filter(g => g.type === 'manual');
  const assigned = new Set((c.groups || []).map(g => g.id));
  document.getElementById('panelGroups').innerHTML = manualGroups.map(g => `
    <label style="display:flex;align-items:center;gap:8px;padding:8px 0;cursor:pointer;font-size:14px;color:var(--text);border-bottom:1px solid var(--border)">
      <input type="checkbox" ${assigned.has(g.id) ? 'checked' : ''} onchange="toggleContactGroup('${c.jid}', ${g.id}, this.checked)">
      ${esc(g.name)}
      <span style="margin-left:auto;font-size:11px;color:var(--text-muted)">${g.reply_mode}</span>
    </label>`).join('') || '<div style="color:var(--text-muted);font-size:13px">No manual groups yet.</div>';
  document.getElementById('contactPanel').style.display = '';
}

function closePanel() {
  document.getElementById('contactPanel').style.display = 'none';
  currentContact = null;
}

async function toggleContactGroup(jid, groupID, add) {
  if (add) {
    await fetch('/api/contacts/' + encodeURIComponent(jid) + '/groups', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({group_id: groupID})
    });
  } else {
    await fetch('/api/contacts/' + encodeURIComponent(jid) + '/groups/' + groupID, {method: 'DELETE'});
  }
  await loadContacts();
  // re-open panel with fresh data
  if (currentContact) {
    const fresh = contacts.find(x => x.jid === jid);
    if (fresh) openContact(jid);
  }
}

function esc(s) {
  return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

init();
</script>` + `</body></html>`
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

- [ ] **Step 3: Commit**

```bash
git add internal/server/html_contacts.go
git commit -m "server: add Contacts tab — group management + contact assignment UI"
```

---

## Task 13: Wire Routes, Nav, and Agent Config UI

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/html_shared.go`
- Modify: `internal/server/agent.go` (expose new config fields)

- [ ] **Step 1: Register new routes in buildMux()**

In `internal/server/server.go`, add to `buildMux()`:

```go
// Contacts + Groups
mux.HandleFunc("/contacts", s.handleContactsPage)
mux.HandleFunc("/api/contacts", s.handleListContacts)
mux.HandleFunc("/api/contacts/", s.handleContactsSubroute) // dispatch /api/contacts/{jid}/groups etc
mux.HandleFunc("/api/groups", s.handleGroupsRoute) // GET + POST
mux.HandleFunc("/api/groups/", s.handleGroupsSubroute) // PUT + DELETE + /contacts sub

// Messages (pending replies)
mux.HandleFunc("/messages", s.handleMessagesPage)
mux.HandleFunc("/api/pending-replies", s.handleListPendingReplies)
mux.HandleFunc("/api/pending-replies/", s.handlePendingRepliesSubroute)
```

Add dispatch helpers in `contacts.go`:

```go
func (s *Server) handleContactsSubroute(w http.ResponseWriter, r *http.Request) {
    // /api/contacts/{jid}/groups
    // /api/contacts/{jid}/groups/{group_id}
    path := r.URL.Path
    if strings.Contains(path, "/groups/") && r.Method == http.MethodDelete {
        s.handleRemoveContactGroup(w, r)
    } else if strings.HasSuffix(path, "/groups") {
        s.handleAssignContactGroup(w, r)
    } else {
        http.NotFound(w, r)
    }
}

func (s *Server) handleGroupsRoute(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        s.handleListGroups(w, r)
    case http.MethodPost:
        s.handleCreateGroup(w, r)
    default:
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleGroupsSubroute(w http.ResponseWriter, r *http.Request) {
    // /api/groups/{id}  PUT/DELETE
    // /api/groups/{id}/contacts  GET
    path := r.URL.Path
    if strings.HasSuffix(path, "/contacts") {
        s.handleListGroupContacts(w, r)
        return
    }
    switch r.Method {
    case http.MethodPut:
        s.handleUpdateGroup(w, r)
    case http.MethodDelete:
        s.handleDeleteGroup(w, r)
    default:
        http.NotFound(w, r)
    }
}
```

Add dispatch helper in `messages.go`:

```go
func (s *Server) handlePendingRepliesSubroute(w http.ResponseWriter, r *http.Request) {
    path := r.URL.Path
    switch {
    case strings.HasSuffix(path, "/approve"):
        s.handleApprovePendingReply(w, r)
    case strings.HasSuffix(path, "/reject"):
        s.handleRejectPendingReply(w, r)
    case strings.HasSuffix(path, "/edit-send"):
        s.handleEditSendPendingReply(w, r)
    default:
        http.NotFound(w, r)
    }
}
```

- [ ] **Step 2: Add Contacts + Messages to nav**

In `internal/server/html_shared.go`, in `navHTML()`, add two entries to the `pages` slice:

```go
{"/contacts", "Contacts"},
{"/messages", "Messages"},
```

- [ ] **Step 3: Expose new config fields in agent config API**

In `internal/server/agent.go`, `handleAgentConfig` GET response, add:
```go
"global_reply_mode":   cfg.GlobalReplyMode,
"owner_jid":          cfg.OwnerJID,
"auto_sync_frequency": cfg.AutoSyncFrequency,
```

In the POST body struct, add:
```go
GlobalReplyMode   string `json:"global_reply_mode"`
OwnerJID          string `json:"owner_jid"`
AutoSyncFrequency string `json:"auto_sync_frequency"`
```

In the POST handler, populate `cfg`:
```go
cfg.GlobalReplyMode   = body.GlobalReplyMode
cfg.OwnerJID          = body.OwnerJID
cfg.AutoSyncFrequency = body.AutoSyncFrequency
```

Add defaults in POST handler:
```go
if cfg.GlobalReplyMode == "" {
    cfg.GlobalReplyMode = "auto"
}
```

- [ ] **Step 4: Build**

```bash
go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/html_shared.go internal/server/agent.go internal/server/contacts.go internal/server/messages.go
git commit -m "server: wire contacts + messages routes, nav tabs, expose new agent config fields"
```

---

## Task 14: Manual Test + Final Commit

- [ ] **Step 1: Start the app**

```bash
go run . --no-tray
```

Expected: starts without errors, dashboard at http://127.0.0.1:10002.

- [ ] **Step 2: Verify session middleware**

Open http://127.0.0.1:10002 in browser. Should redirect to `/login` showing "Send !login via WhatsApp".

Note: for local development without WhatsApp connected, you can temporarily bypass by commenting out the session middleware redirect and re-enabling after testing.

- [ ] **Step 3: Verify Contacts tab**

Navigate to `/contacts` — Groups view loads, empty state shown. Create a manual group "VIP Clients" with reply_mode=auto. Verify it appears in the table.

- [ ] **Step 4: Verify Messages tab**

Navigate to `/messages` — Pending tab shows empty state. Sent tab shows empty state.

- [ ] **Step 5: Verify agent config API**

```bash
curl -s http://127.0.0.1:10002/api/agent/config | python3 -m json.tool
```

Expected: JSON includes `global_reply_mode`, `owner_jid`, `auto_sync_frequency`.

- [ ] **Step 6: Push**

```bash
git push
```

---

## Self-Review Notes

**Spec coverage check:**
- [x] Contact groups (manual + auto) — Tasks 1-2
- [x] Per-group + global reply mode — Tasks 4-6 + resolver
- [x] Review-first pending reply flow — Tasks 3, 6, 10
- [x] WhatsApp owner notification (batched 5min) — Task 6 `sendOwnerNotification`
- [x] Messages tab batch review UI — Tasks 10-11
- [x] Magic link auth (!login → token → session cookie) — Tasks 7-8
- [x] Contact upsert on first message — Task 6 `UpsertContact` in `process()`
- [x] Contacts tab with group assignment — Tasks 9, 12
- [x] Nav entries — Task 13
- [x] Agent config OwnerJID + GlobalReplyMode fields — Task 4, 13

**Known simplifications:**
- Auto-group sync job framework: routes and frequency config are wired, but the actual business logic (what contract data → which group) is a stub — to be implemented when the data format is known.
- Session store is in-memory (lost on restart); dad must send `!login` again after app restart. Acceptable for single-user use.
