# Dad's Dispatch Agent — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan phase-by-phase. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let dad chat with the server over Telegram or WhatsApp, dispatch work in natural language, and have the agent run real actions (send messages, search KB, generate images, update client profiles, broadcast). Build on existing crm-agent rails — no rebuild.

**Architecture:** Existing crm-agent Go binary stays the host. Add Telegram connector + dispatch loop + image/profile/Obsidian/GDrive packages alongside. Owner channels (WA + TG) route free-text to a Claude run with MCP tools loaded; Claude picks tools, executes, replies. Same MCP server already used by Claude Desktop.

**Tech Stack:**
- Go (existing): all `internal/*` packages
- Telegram: Bot API (long-poll, no webhook needed — local NAT)
- WhatsApp: whatsmeow (existing personal account) + Business Cloud API (P8 only)
- LLM: existing `internal/claude/client.go` (CLI wrapper, subscription auth). Optional swap to Anthropic API key in P-Late
- Image: Google nano-banana-2 (gemini-3.1-flash-image-preview)
- KB pipeline: existing `internal/knowledge/` (embeddings, watcher)
- Obsidian: file writer only (one-way)
- GDrive: OAuth + folder sync → existing watcher path
- Persistence: existing SQLite

**Out of scope for v1 (defer):**
- WhatsApp Business Cloud API integration (P8 — separate phase, big rock)
- Voice input/output
- Cross-device sync (file-based via iCloud / Obsidian Sync is fine — no app code)
- Multi-turn dispatch memory (each owner message = stateless Claude run for v1)
- Cron / scheduled dispatches (v2)

---

## Phase Overview

| Phase | Deliverable | Effort |
|---|---|---|
| **P0** | Merge `worktree-whatsapp-broadcast` → main | 1h |
| **P1** | Telegram connector + owner allowlist + echo | 1d |
| **P2** | Dispatch loop (WA owner + TG → Claude+MCP) | 1d |
| **P3** | `client_profile` schema + passive extraction agent | 2d |
| **P4** | Image generation MCP tool (nano-banana-2) | 1d |
| **P5** | Obsidian sync (writer only) | 1.5d |
| **P6** | Personalized message v2 — uses profile data | 0.5d |
| **P7a** | GDrive OAuth + folder watcher → KB pipeline | 1.5d |
| **P7b** | Local folder add-to-KB UI | 0.5d |
| **P8** | WhatsApp Business Cloud API (separate plan) | 3-5d |

Total P0–P7: ~9 days.

---

## Phase 0: Merge broadcast worktree

**Files:** none (git-only)

- [ ] **Step 1: Verify worktree state**

```bash
cd .claude/worktrees/whatsapp-broadcast
git status
go build ./...
go test ./internal/broadcast/...
```
Expected: clean, build ok, tests pass.

- [ ] **Step 2: Merge to main**

```bash
cd /Users/gen/Documents/Claude/Projects/Personal\ Assistant\ App/crm-agent
git checkout main
git merge worktree-whatsapp-broadcast --no-ff -m "Merge WhatsApp broadcast loop (14 tasks)"
go build ./...
```

- [ ] **Step 3: Smoke test**

Launch app, submit a 3-message batch via dashboard, confirm `/broadcasts/{id}` shows progress.

- [ ] **Step 4: Cleanup**

```bash
git worktree remove .claude/worktrees/whatsapp-broadcast
git branch -d worktree-whatsapp-broadcast
```

---

## Phase 1: Telegram connector

**Files:**
- Create: `internal/connectors/telegram/telegram.go`
- Create: `internal/connectors/telegram/telegram_test.go`
- Modify: `internal/agent/config.go` — add `telegram_bot_token`, `owner_telegram_ids`
- Modify: `internal/server/server.go` — start TG connector on app boot
- Modify: `internal/server/html_agent.go` — settings UI

- [ ] **Step 1: Bot API client**

Implement long-poll `getUpdates`, `sendMessage`, `sendPhoto`, `sendDocument`. No third-party SDK — Telegram Bot API is plain JSON over HTTP. Token from `agent_config.telegram_bot_token`.

- [ ] **Step 2: Owner allowlist**

On each update, check `update.message.from.id` ∈ `owner_telegram_ids`. Drop silently if not.

- [ ] **Step 3: Echo handler (for verification before P2 lands)**

Reply with `"Got: <msg>"`. Replaced in P2 by dispatch loop.

- [ ] **Step 4: Settings UI**

Agent page: bot token field, owner IDs (comma-sep), "Test connection" button.

- [ ] **Step 5: Tests**

Unit-test JSON marshal/unmarshal, allowlist filter, retry-on-conflict logic.

- [ ] **Step 6: Smoke test**

Create bot via @BotFather, paste token + own TG ID, send "hi" → bot echoes.

- [ ] **Step 7: Commit per logical piece**

---

## Phase 2: Dispatch loop

**Files:**
- Create: `internal/agent/dispatch.go`
- Create: `internal/agent/dispatch_test.go`
- Modify: `internal/agent/replier.go` — route owner free-text to dispatch instead of reply gen
- Modify: `internal/connectors/telegram/telegram.go` — call dispatch on inbound msg

- [ ] **Step 1: Define dispatch API**

```go
type Dispatcher struct {
    Claude        claudeRunner    // runs Claude CLI in print mode
    MCPConfigPath string          // path to bridge's MCP config (claude already knows)
}

type DispatchInput struct {
    OwnerChannel string // "telegram" / "whatsapp"
    OwnerID      string // tg_id or jid
    Message      string
    Attachments  []string // file paths if any
}

type DispatchResult struct {
    Reply      string
    ToolCalls  []string // names of MCP tools called, for audit log
    DurationMS int
}

func (d *Dispatcher) Run(ctx context.Context, in DispatchInput) (DispatchResult, error)
```

- [ ] **Step 2: Implement Run**

Shell out to `claude --print --mcp-config <bridge>` with the message as user prompt. Capture stdout. Parse final text + tool-call list (claude CLI emits both).

Add system prompt:
```
You are dad's dispatch agent. The owner sent you a message via {{channel}}.
You have MCP tools to act on WhatsApp, Facebook, knowledge base, broadcasts, images.
Pick tools, execute, then reply with a short status update.
If unsure, ask one clarifying question. Never act on anything destructive without explicit confirmation in the same message.
```

- [ ] **Step 3: Wire WhatsApp owner path**

In `replier.go`: if `from_jid` ∈ `owner_jids` AND msg starts with `/` or matches dispatch heuristic → call `Dispatcher.Run` instead of generating a reply.

- [ ] **Step 4: Wire Telegram path**

In `telegram.go` message handler → `Dispatcher.Run` → `sendMessage(reply)`.

- [ ] **Step 5: Audit log**

New SQLite table `dispatch_log`: id, channel, owner_id, message, reply, tools_used (json), duration_ms, timestamp.

- [ ] **Step 6: Tests**

Mock `claudeRunner`, verify prompt assembly + allowlist enforcement + log write.

- [ ] **Step 7: Smoke**

Send via TG: "what tools do you have?" — Claude lists MCP tools.
Send: "send 'hi' to <test jid> on whatsapp" — Claude calls `send_whatsapp_message`.

---

## Phase 3: Client profile + passive extraction

**Files:**
- Create: migration `client_profiles` table in `internal/store/store.go`
- Create: `internal/profile/profile.go` (CRUD)
- Create: `internal/profile/extractor.go` (Claude-based)
- Create: `internal/profile/extractor_test.go`
- Modify: `internal/agent/runner.go` — trigger extractor on owner-flagged messages
- Modify: `internal/mcp/mcp.go` — expose `get_client_profile`, `update_client_profile` tools

- [ ] **Step 1: Schema**

```sql
CREATE TABLE client_profiles (
    jid TEXT PRIMARY KEY,
    display_name TEXT,
    aliases TEXT,        -- json array
    language TEXT,
    role TEXT,           -- "lead", "client", "family", etc.
    family_notes TEXT,
    interests TEXT,      -- json array
    last_topics TEXT,    -- json array, most-recent first
    custom_notes TEXT,
    updated_at INTEGER,
    extracted_at INTEGER
);
CREATE INDEX idx_profiles_role ON client_profiles(role);
```

- [ ] **Step 2: CRUD package**

Standard Get/Upsert/List/Search by alias.

- [ ] **Step 3: Extractor**

`Extract(jid, recentMessages []string, existing *Profile) (*Profile, error)`. Calls Claude with JSON schema, merges into existing profile (no overwrite of `custom_notes` from owner).

- [ ] **Step 4: Trigger policy**

Run extractor when owner mentions a contact name in dispatch, OR nightly cron over last 24h of inbound messages. Skip extractor for chatty contacts who haven't been mentioned recently.

- [ ] **Step 5: MCP tools**

`get_client_profile(jid_or_name)` — for dispatch ("tell me about Alice")
`update_client_profile(jid, field, value)` — for dispatch ("note that Bob has a daughter named Sara")

- [ ] **Step 6: Tests + smoke**

Send 10 fake msgs from a contact via WA, run extractor, inspect resulting profile. Dispatch: "what do you know about <contact>?" → reads profile.

---

## Phase 4: Image generation MCP tool

**Files:**
- Create: `internal/image/gemini.go` (nano-banana-2 API client)
- Create: `internal/image/gemini_test.go`
- Modify: `internal/agent/config.go` — `gemini_api_key`
- Modify: `internal/mcp/mcp.go` — `generate_image` tool

- [ ] **Step 1: Gemini client**

POST to `gemini-3.1-flash-image-preview`. Input: prompt, optional reference image (b64). Output: png bytes. Save to `data/images/<uuid>.png`, return path.

- [ ] **Step 2: MCP tool**

`generate_image(prompt, aspect_ratio="16:9", reference_image_path=null)` — returns local path. Aspect ratio enforced via prompt wrapper.

- [ ] **Step 3: Cost guard**

Per-day spend cap (configurable, default $5). Counter in SQLite. Log every call.

- [ ] **Step 4: Dispatch hookup**

Owner says "draw me X" via TG → Claude calls `generate_image` → server attaches resulting file via TG `sendPhoto`. Same for WA.

- [ ] **Step 5: Smoke**

TG: "make me a poster for shop opening" → image arrives.

---

## Phase 5: Obsidian sync (writer only)

**Files:**
- Create: `internal/obsidian/obsidian.go`
- Create: `internal/obsidian/obsidian_test.go`
- Modify: `internal/agent/config.go` — `obsidian_vault_path` (optional)
- Modify: `internal/profile/profile.go` — call `obsidian.WriteProfile` on Upsert

- [ ] **Step 1: Template**

One `.md` file per client at `<vault>/Clients/<DisplayName>.md`:

```markdown
---
jid: 60123456789@s.whatsapp.net
role: client
interests: [insurance, family-protection]
last_updated: 2026-05-15
---

# {{name}}

## Family
{{family_notes}}

## Recent Topics
- [[Topic A]]
- [[Topic B]]

## Custom Notes
{{custom_notes}}

## Recent Conversations
- 2026-05-14 — asked about policy renewal
```

`[[Topic A]]` syntax = wikilink → Obsidian graph view shows connections.

- [ ] **Step 2: Topic files**

Each `last_topics` entry also gets `<vault>/Topics/<Topic>.md` (stub if missing). Cross-link from client side via wikilink.

- [ ] **Step 3: Owner-edit warning**

File header: `> Auto-generated. Edits to this file are overwritten on next profile update. Add manual notes under "## Custom Notes" only.`

- [ ] **Step 4: Tests**

Verify file written, frontmatter valid YAML, wikilinks formatted correctly.

- [ ] **Step 5: Smoke**

Update a profile, open Obsidian vault, confirm file present + graph view shows links.

---

## Phase 6: Personalized message v2

**Files:**
- Modify: `internal/broadcast/personalize.go` — pull profile data

- [ ] **Step 1: Extend Input struct**

Add `Profile *profile.Profile` field. Builder pulls it from store when available.

- [ ] **Step 2: Update prompt**

System prompt becomes:
```
Personalize this message for the contact. Their profile:
  Name: {{name}}
  Role: {{role}}
  Interests: {{interests}}
  Family: {{family_notes}}
  Recent topics: {{last_topics}}
  Custom notes: {{custom_notes}}

Base message: {{template}}
Reply with only the rephrased message.
```

- [ ] **Step 3: Smoke test**

Run broadcast to 3 contacts with profiles — each gets distinctly tailored msg.

---

## Phase 7a: GDrive sync → KB

**Files:**
- Create: `internal/connectors/gdrive/gdrive.go`
- Create: `internal/server/html_gdrive.go`
- Modify: `internal/server/server.go` — register routes

- [ ] **Step 1: OAuth flow**

Use `golang.org/x/oauth2/google` + Drive API v3. Read-only scope. Redirect URL = `http://127.0.0.1:10002/gdrive/callback`. Token stored encrypted via existing credential path.

- [ ] **Step 2: Folder picker**

UI: list root folders, user picks one to mirror. Save folder ID.

- [ ] **Step 3: Sync worker**

Periodic poll (every 10 min). For each file in chosen folder: download to `data/gdrive_mirror/<file>`, update timestamp. Existing `internal/knowledge/watcher.go` picks up changes → embeds → searchable.

- [ ] **Step 4: Smoke**

Drop a PDF in the GDrive folder, wait 10 min, dispatch: "what's in the new compliance doc?" → Claude calls `search_documents`, returns content.

## Phase 7b: Local folder add to KB

**Files:**
- Modify: `internal/server/html_knowledge.go` — add "Watch local folder" button

- [ ] **Step 1: UI**

Folder path input + add to watcher list. Existing watcher already supports multiple paths.

- [ ] **Step 2: Smoke**

---

## Phase 8: WhatsApp Business Cloud API (separate plan)

**Defer.** Writes a separate plan once P0–P7 land. Requires:
- Meta developer account + WhatsApp Business app
- Webhook public URL (cloudflared tunnel or hosted)
- Template message approval workflow
- Phone number provisioning

Parallel to whatsmeow connector; user picks per-send.

---

## Self-Review Checklist

- [ ] All phases have concrete file paths
- [ ] No "TBD" placeholders
- [ ] Each phase ends with smoke test + commits
- [ ] Owner allowlist enforced at every entry point (TG, WA, dispatch)
- [ ] Audit log captures every dispatch action
- [ ] Cost caps on image gen + Claude usage (if API key lane adopted)
- [ ] Obsidian is one-way write (no read-back to avoid edit conflicts)
- [ ] No new dependencies beyond what's strictly required

---

## Known Limitations (communicate when shipping)

1. **Stateless dispatch v1** — each message = fresh Claude run, no memory of prior turns. Add conversation memory in v2.
2. **Subscription Claude CLI rate limits** — heavy server use may trip limits. Monitor; switch to API key if needed.
3. **Telegram long-poll only** — one bot instance at a time. If running two servers, second will steal updates.
4. **Obsidian writer overwrites manual edits** outside `## Custom Notes`. Document clearly.
5. **GDrive sync is poll-based** (10 min). Real-time push needs webhook tunnel.
