# Owner Profile — shared role for the dispatch bot, synced via MCP

- **Date:** 2026-05-27
- **Status:** Approved (design)
- **Topic:** Give the Telegram dispatch bot a configurable role/persona ("owner profile"), stored as a single vault file, readable and writable from Cowork (MCP), Obsidian (direct edit), and the bot itself.

## Problem

The Telegram dispatch bot's behavior is fixed by the hardcoded `dispatchSystemPrompt` (`internal/agent/dispatch.go`). It has no persona or owner context. The owner ("dad") has already authored a profile inside the Claude **Cowork** desktop app, but crm-agent runs a *separate* Claude instance (`claude --print`), so that profile is invisible to the bot.

Goal: one owner profile that is the single source of truth, used by the dispatch bot as its role, and editable from all three surfaces that touch it — Cowork, Obsidian, and the bot — without sync drift.

## Non-goals

- **Per-owner profiles.** There is one owner (dad). A single global profile. Multiple `OwnerTelegramIDs` all share it. (Revisit only if a real multi-owner need appears.)
- **Replacing the dispatch action schema.** `dispatchSystemPrompt` defines the JSON wire contract between Claude and the executor. The profile *augments* it; it never replaces it.
- **Auto-importing Cowork's internal project instructions.** MCP is the app calling *out* to tools; it cannot read or push into Cowork's own custom instructions. The profile is re-homed into the vault file once (see "Cowork hookup").
- **Versioning / backup of the profile.** The vault is editable in Obsidian and may be git-backed; that is the history mechanism. No in-app version store.

## Architecture

One file is the source of truth. Three front doors write it; the bot reads it.

```
SET ONCE:   Cowork ──update_owner_profile (MCP)──►┐
UPDATE via: Obsidian ──edit file directly────────►│   <vault>/Profile.md
UPDATE via: Telegram bot ──update_owner_profile──►┘          │
                                                             ▼
                                       dispatch bot reads it each turn = its role
```

All writes converge on `ownerprofile.Write`, so there is exactly one code path that mutates the file (plus Obsidian editing the bytes directly). No second copy, no sync job.

### Profile file

- **Location:** the Obsidian vault directory, filename `Profile.md`. The vault dir is the same path already resolved for the dispatcher's Obsidian reader (the `Vault/` subdir of the knowledge `FolderPath`). Living inside the vault means dad can edit it as a normal Obsidian note and `read_note`/`read_kb` already see it.
- **Format:** free-form Markdown. No schema imposed — it is a persona/context document (who the owner is, tone, how the bot should behave, business context). Whatever dad wrote in Cowork pastes in directly.
- **Missing file:** treated as empty profile. The bot falls back to exactly today's behavior.
- **Auto-create:** on startup the app ensures an empty (or templated) `Profile.md` exists, reusing the existing vault auto-create pattern, so dad has a note to open. A first `update_owner_profile` also creates it.

## Components

Each unit has one job and a narrow interface.

### 1. `internal/ownerprofile` (new)

The single read/write store for the profile file. Mirrors `folderread.Root`'s disabled-when-empty pattern, but — unlike `folderread`, which is **read-only** — it also writes.

```go
type Store struct{ path string } // resolved absolute path to <vault>/Profile.md

func New(vaultDir string) *Store        // empty vaultDir → disabled store
func (s *Store) Enabled() bool
func (s *Store) Read() (string, error)  // missing file → ("", nil)
func (s *Store) Write(content, mode string) error // mode: "replace" (default) | "append"; creates file + parent dir
```

- Fixed filename, no caller-supplied path component → no traversal surface.
- `Read` on a missing file returns empty string, not an error.
- `Write` creates the parent directory if needed (vault auto-create reuse).

### 2. Dispatcher role injection (`internal/agent/dispatch.go`)

The `Dispatcher` gains one optional field:

```go
OwnerProfile func() (string, error) // nil → today's behavior
```

On each `Run`, before calling `d.Claude.Reply`, read the profile. If non-empty, prepend it to the system prompt as a delimited block ahead of the action schema:

```
## Owner profile
<contents of Profile.md>

---
<existing dispatchSystemPrompt>
```

The JSON schema and rules in `dispatchSystemPrompt` are unchanged and remain last so they dominate output formatting. A read error degrades gracefully to the base prompt (logged, not fatal) — consistent with how memory resolution already degrades.

### 3. MCP tools (`internal/mcp/mcp.go`) — for Cowork

Two new tools alongside the existing WhatsApp/Facebook/knowledge set:

- `get_owner_profile` — no args → returns current `Profile.md` contents.
- `update_owner_profile` — `{ "content": string, "mode": "replace" | "append" }` (mode optional, default `replace`) → writes via the `ownerprofile` store.

Both handlers call the same `ownerprofile.Store`. This is how Cowork pushes the profile in ("set once") and updates it later.

### 4. Dispatch actions (`internal/agent/dispatch.go`) — for the bot/owner

Two new actions so the owner can view/save the profile from Telegram:

- `get_owner_profile` — `{}` → returns current profile.
- `update_owner_profile` — `{ "content": string, "mode": "replace" | "append" }` → writes it.

Naming uses the `owner_` prefix to avoid collision with the existing **client**-profile actions `get_profile` / `update_profile`. These actions call the `ownerprofile.Store` directly — the bot does **not** JSON-RPC its own MCP server.

Requires: a new `Executor` method pair (e.g. `GetOwnerProfile(ctx) (string, error)` and `UpdateOwnerProfile(ctx, content, mode string) error`), const action names, `execute` handlers, catalog + system-prompt schema entries. Follows the documented "adding a new action" recipe at the top of `dispatch.go`.

Typical bot flow for "update my profile to add X" reuses the existing `continue: true` chaining: `get_owner_profile` (continue) → merge → `update_owner_profile` (replace) → `reply`.

## Data flow / sync

- **Set once (Cowork):** dad's Cowork project calls `update_owner_profile` with his profile text → vault file created/overwritten.
- **Update from Obsidian:** dad edits `Profile.md` directly. No code path; the bot reads the new bytes next turn.
- **Update from the bot:** owner texts a profile change → bot reads current, merges, writes via the dispatch action.
- **Read (bot role):** every dispatch `Run` reads the current file and injects it.

Because every writer hits one file, any surface sees the others' latest write on next read. No background sync, no conflict resolution beyond last-write-wins (acceptable for a single-owner profile doc).

## Cowork hookup (the one manual step)

MCP cannot auto-load the profile into a Cowork conversation. The owner adds one line to the Cowork project instructions, e.g.:

> "At the start of a conversation call `get_owner_profile` to load my profile. When I ask you to change my profile, call `update_owner_profile`."

This is a documentation/setup note, not code. It is the only manual part of the bidirectional sync.

## Error handling & security

- Missing file → empty profile everywhere; bot uses base prompt.
- Disabled store (no vault configured) → `get_owner_profile` returns empty, `update_owner_profile` returns a clear "no vault configured" error; bot role injection is skipped.
- Fixed filename + no caller path input → no path traversal.
- Dispatcher profile-read error is non-fatal (degrade to base prompt, log).
- `update_owner_profile` with `replace` is destructive by design; Obsidian/git history is the recovery path (stated non-goal).

## Testing (TDD)

- **`ownerprofile`:** read present; read missing → `("", nil)`; write replace overwrites; write append appends; write creates missing parent dir; disabled store behavior.
- **dispatcher:** with `OwnerProfile` returning text → captured system prompt (via fake `ClaudeRunner`) contains the `## Owner profile` block and still contains the full action schema; with nil/empty → system prompt unchanged from today; profile-read error → base prompt, no crash.
- **dispatch actions:** `get_owner_profile` returns store content; `update_owner_profile` writes through the fake executor; bad params surfaced.
- **mcp:** `get_owner_profile` returns content; `update_owner_profile` writes; missing `content` → error.

## Future (out of scope now)

- Per-owner profiles keyed by Telegram ID.
- A dashboard editor for the profile (today: Obsidian or the bot).
- Profile templating / structured fields.
