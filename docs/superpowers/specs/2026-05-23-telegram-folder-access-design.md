# Telegram Dispatch Agent — Folder Access, Obsidian Reader & Multi-Step Loop

**Date:** 2026-05-23
**Status:** Design approved, pending spec review

## Problem

The owner reports the Telegram agent is "stupid" and "can't access folders at all,"
despite the app having a "share knowledge base folder" option. Two perceived bugs:

1. The agent can't read files in the shared knowledge base folder.
2. Responses always arrive after ~4 seconds, feeling like a fixed wait with no real
   answer.

### Root-cause findings (from code reconnaissance)

- **Dispatcher is one-shot.** Telegram messages route to `internal/agent/dispatch.go`
  `Dispatcher.Run`, which calls `claude --print --model claude-haiku-4-5` once
  (`internal/claude/client.go:90`), parses ONE JSON action, executes it, and returns.
  There is no tool-use loop and no way to chain actions.
- **No raw folder access.** The dispatcher has a `search_kb` action (indexed FTS over
  classified docs) but **no way to browse or read raw files** in the shared knowledge
  folder. By contrast, the cowork folder has full `list/read/search/edit`. This
  asymmetry is the "can't access folders" complaint.
- **The 4s is real, not fake.** The typing indicator (`main.go:445`) re-sends every 4s
  because Telegram clears it after 5s — this is cosmetic. The actual reply latency is
  the genuine cost of spawning a fresh `claude --print` subprocess running haiku per
  message. There is no `time.Sleep` anywhere in the path.
- **Why it feels empty:** weak model (haiku) + one-shot + no folder tools means it can
  only chat or pick one canned action. It cannot read-then-act.
- **Obsidian is write-only today.** `internal/obsidian` renders Client/Topic markdown
  into the vault (`Writer`). There is no read path.
- **Shared Claude client.** `knowClient` (haiku) is reused by the doc classifier,
  auto-reply Replier, profile extractor, AND the dispatcher (`main.go:592`). The model
  cannot be bumped globally without slowing/over-costing the bulk indexing pipeline.

## Acceptance test (owner's own)

> Hook up a folder in the knowledge base settings, ask Telegram to read its content,
> then have it write a WhatsApp message to one of my contacts about the finding.

This is a **single instruction that chains two actions**: `read_kb` → `send_whatsapp`.
The current one-shot dispatcher cannot satisfy it. The multi-step loop below is what
makes this test pass.

## Decisions

| Question | Decision |
|----------|----------|
| Root cause to fix | Missing folder tools (keep constrained-action design, not full agentic rebuild) |
| KB access model | Both layered — keep indexed `search_kb` + add raw browse/read |
| Obsidian capability | Obsidian-aware reader (resolve `[[wikilinks]]`, backlinks, `#tags`, frontmatter) |
| Obsidian timing | Build now (same effort) |
| Model | Bump dispatch-only to sonnet; classifier/Replier/extractor stay haiku |
| Action chaining | Add a bounded multi-step loop so one instruction can read-then-act |
| Session boundary | Idle gap > 1h ends a session; ≤ 1h continues the same session |
| In-session memory | "Infinite" — rolling summary + raw turns since last compaction |
| Compaction | Background cron, only when the owner is idle/offline; raw turns pruned, replaced by summary |
| Cross-session recall | Model-driven `recall_memory {query}` action over stored session summaries |

## Design

### 1. Multi-step dispatch loop (the linchpin)

Rework `Dispatcher.Run` from one-shot into a **bounded loop** (`maxSteps = 5`):

```
transcript := buildDispatchUserPrompt(in, recentTurns)
trail := []string            // executed actions, for the final status line
for step := 1; step <= maxSteps; step++ {
    raw := Claude.Reply(systemPrompt, transcript)
    parsed, err := parseDispatch(raw)
    if err != nil { finalReply = raw; break }          // existing tolerant fallback
    if parsed.Action == reply { finalReply = parsed.UserReply; break }

    result := executeStep(parsed)                       // {observation, userStatus, err}
    trail = append(trail, string(parsed.Action))
    transcript += "\n\n[YOU CHOSE]: " + raw +
                  "\n[RESULT]: " + result.observation +
                  "\nIf the task is done, reply with action \"reply\" and your message " +
                  "to the owner. Otherwise issue the next action."
}
// finalReply (+ compact trail e.g. "[read_kb → send_whatsapp]") sent to owner
```

Key points:

- **Result feedback.** Today `execute` returns only a short user-facing suffix
  (e.g. `(sent to Alice)`). Refactor each action handler to return an
  `actionResult{ observation string; userStatus string }`. For most actions
  `observation == userStatus`; for **read actions** `observation` carries the actual
  content (capped at a model-context limit, ~8000 chars) so the next step can use it.
- **Only the final reply is sent to the owner.** Intermediate `user_reply` fields are
  not sent (the typing indicator covers the wait). The final `reply` action's text is
  sent, with a compact action trail appended for transparency.
- **`reply` is the terminator.** No new sentinel needed — the existing `reply` action
  ends the loop. The system prompt instructs the model to finish with `reply` once the
  task is complete.
- **Step cap = 5.** Covers e.g. list → read → act → finish with headroom. Each
  sonnet call is several seconds; worst case ~30-40s. Acceptable; typing indicator
  already refreshes during the wait. Documented as a known latency cost.
- **Safety preserved.** Existing rule — destructive or large-scale (>20 recipients)
  actions require the model to `reply` and ask for confirmation rather than execute —
  stays in the system prompt. The step cap bounds runaway loops. Input is already
  owner-allowlisted.
- **Logging.** One `SaveDispatchLog` at the end; the `action` field records the chain
  (e.g. `read_kb→send_whatsapp`); duration covers the whole loop.

### 2. New actions

Six new actions, following the existing per-verb idiom (matches cowork; clear
descriptions guide the model better than one overloaded action):

| Action | Backed by | Params | Returns |
|--------|-----------|--------|---------|
| `list_kb` | raw KB folder | `{subdir?}` | entries (files + subdirs) in subdir, non-recursive, newest-first |
| `read_kb` | raw KB folder | `{path}` | file content by relative path, any file type |
| `read_note` | Obsidian vault | `{name}` | frontmatter + body + outgoing `[[links]]` + `#tags` |
| `backlinks` | Obsidian vault | `{name}` | notes that link TO the named note |
| `search_notes` | Obsidian vault | `{query?, tag?}` | grep hits by text and/or `#tag` (empty = browse) |
| `recall_memory` | session summaries | `{query}` | matching past-session summaries (see Section 6) |

The existing `search_kb` (indexed FTS) is kept as the smart-find layer. Raw
`list_kb`/`read_kb` add the browse/read capability that was missing. `recall_memory`
is the model-driven cross-session recall hook.

### 3. New & changed components

**NEW `internal/folderread/folderread.go`** — generic, read-only folder access:

- `Root{ Path string }`; `New(path)`; `Enabled()`.
- `List(subdir string) ([]Entry, error)` — entries in `Path/subdir`, skips dotfiles,
  marks `IsDir`/`IsText`, sorts newest-first. Missing dir → empty, not error.
- `Read(relpath string) (text string, e *Entry, err error)` — reads a file; binary
  files return a placeholder; text capped at a generous model-context limit.
- **Path-traversal guard:** clean the joined path and verify it stays within `Path`
  (reject `..` escapes, absolute paths, symlink escape). This is essential — unlike
  cowork (date-folder scoped), `read_kb` takes an arbitrary relative path.
- No imports of the rest of the app; wired via the dispatch Executor.

**`internal/obsidian/reader.go`** (NEW, beside the existing write-only `Writer`):

- `Reader{ VaultPath string }`; `New(vaultPath)`; `Enabled()`.
- `type Note struct { Name, Path string; Frontmatter map[string]string; Body string; OutLinks, Tags []string }`
- `ReadNote(name) (*Note, error)` — resolve `name` (strip `[[ ]]`, accept bare `Note`
  or `folder/Note`, fuzzy-match `*.md` across the vault), parse YAML frontmatter
  (`--- … ---`), extract `[[wikilinks]]` (drop `|alias` and `#heading`), extract
  `#tags`. Body capped for model context.
- `Backlinks(name) ([]string, error)` — walk vault `*.md`, collect files containing
  `[[name]]` (base-name match). On-demand scan (vault is small); caching deferred.
- `Search(query, tag string) ([]Hit, error)` — walk `*.md`, return line hits matching
  `query` (case-insensitive) and/or carrying `#tag`. Capped result count.
- **Confined to `VaultPath`** with the same traversal guard.
- Operates over the **whole vault** (Clients/, Topics/, Cowork/, hand-written notes).

**`internal/agent/dispatch.go`:**

- 6 new `Action` consts; 6 new `Executor` interface methods (incl. `RecallMemory`).
- New result type `actionResult{ observation, userStatus string }`; `execute` → returns
  it (existing actions adapted: `observation == userStatus`).
- `Run` reworked into the bounded loop (Section 1).
- Session-aware prompt assembly: replace `recentTurns` (flat window) with
  session resolution + rolling-summary-plus-tail context (Section 6).
- System prompt + `actionCatalog`: add the 6 actions with crisp descriptions, the loop
  instruction ("you may chain actions; finish with `reply`"), and guidance to use
  `recall_memory` when the owner references something from a past conversation.

**`internal/agent/compactor.go`** (NEW) — `SessionCompactor{ store, summarizer (haiku
ClaudeRunner), interval, idleThreshold }`; `Start(ctx)` runs the ticker loop; per tick
calls `store.IdleSessionsToCompact` and summarizes each (Section 7).

**`internal/store/store.go`:**

- New `dispatch_sessions` table + `session_id` column on `dispatch_log` (Section 6),
  added as forward-only `CREATE TABLE IF NOT EXISTS` / `ALTER TABLE` migrations
  alongside the existing schema block.
- FTS5 virtual table over `dispatch_sessions.summary` for `recall_memory` search.
- New methods listed in Section 6.

**`main.go`:**

- **Separate dispatch client:** `dispatchClient := claude.New("", "claude-sonnet-4-6")`
  passed to `NewDispatcher` (replaces `knowClient` there). Everything else keeps
  `knowClient` (haiku).
- Wire into `dispatchExecutor`: a `folderread.Root` for the KB folder, an
  `obsidian.Reader` for the vault, and the store (for `RecallMemory`).
- New `dispatchExecutor` methods: `ListKB`, `ReadKB`, `ReadNote`, `Backlinks`,
  `SearchNotes`, `RecallMemory`, returning the agent-package view types.
- Construct and `Start` the `SessionCompactor` (using `knowClient` as the haiku
  summarizer), like `knowPipeline.Start()`.

### 4. Runtime path resolution

The KB folder (`knowledge.Config.FolderPath`) and vault
(`agent.Config.ObsidianVaultPath`) can both change at runtime via the dashboard.
Unlike cowork/obsidianWriter (wired once at startup), the new executor methods
**resolve their root path per call** via a cheap `LoadConfig`, so dashboard edits take
effect without a restart. If a path is unset, return a clear error:
"Set a knowledge base folder (or Obsidian vault) on the Dashboard first."

### 5. Data flow (acceptance test walk-through)

```
Telegram: "read the folder I just shared and message Alice about the key finding"
  → dispatcher.Run, sonnet
  step 1: action list_kb {}            → observation: [report.md, data.csv, …]
  step 2: action read_kb {path:"report.md"} → observation: <file content>
  step 3: action send_whatsapp {name:"Alice", message:"<composed from content>"}
                                       → observation: (sent to Alice)
  step 4: action reply {user_reply:"Read report.md and messaged Alice the summary."}
  → final reply sent, trail "[list_kb → read_kb → send_whatsapp]"
```

### 6. Session memory model

Replaces the flat 30-min / 5-turn window (`memoryWindow`/`memoryTurns` in
`dispatch.go:217`). Sessions are keyed by `(channel, owner_id)` — Telegram and
WhatsApp owner conversations are tracked independently.

**Session boundary (idle gap).** On each inbound message, look up the owner's most
recent dispatch_log row. If `now - last_at > 1h`, start a **new session** (new
`session_id`); otherwise continue the current one. The 1h rule is an *idle* gap, not a
session length cap — a continuous back-and-forth stays one session indefinitely.

**In-session prompt assembly ("infinite within the burst").** The prompt context is:

```
[rolling summary of this session up to summary_through_log_id]   (if any)
+ all raw turns after summary_through_log_id                      (uncompacted tail)
```

During an active burst, raw turns accumulate and are all included (feels infinite).
The background compactor (Section 7) folds them into the rolling summary once the owner
goes idle, keeping the prompt bounded between bursts.

**Storage (new `dispatch_sessions` table):**

| Column | Purpose |
|--------|---------|
| `id` | session id (PK) |
| `channel`, `owner_id` | session key |
| `started_at`, `last_at` | bounds; `last_at` drives the idle/gap checks |
| `summary` | rolling natural-language summary of the session so far |
| `summary_through_log_id` | last dispatch_log row covered by `summary` |
| `summary_at` | when the summary was last refreshed |

`dispatch_log` gains a `session_id` column (FK). New store methods:
`CurrentSession(channel, owner)`, `StartSession(...)`, `AppendNote`/reuse
`SaveDispatchLog` with session_id, `SessionTurnsSince(session_id, log_id)`,
`UpdateSessionSummary(...)`, `IdleSessionsToCompact(idleThreshold)`,
`SearchSessionSummaries(query)`.

**Cross-session recall (`recall_memory` action).** New sessions load **nothing** from
the past by default. When the owner references something prior ("what did we decide
about the Tan policy?"), the model issues `recall_memory {query}`. The executor runs an
FTS5 search over `dispatch_sessions.summary` (optional vector re-rank via the existing
Ollama embedder if available) and feeds the top matches back into the loop. This
satisfies "only read if the new session asks for related items" — recall is the model's
explicit decision, triggered by the message content, not an automatic preload.

### 7. Background compaction (cron)

A new in-process scheduler — `SessionCompactor`, started in `main.go` as a goroutine
ticker like `knowPipeline.Start()` (this is the "cron job running" the owner asked for;
in-process for a long-lived daemon, not OS cron). It is the only place summarization
happens — never on the request path, so chat latency is unaffected.

- **Cadence:** ticks every ~10 min (tunable). On each tick it asks the store for
  sessions that are **idle/offline** and have uncompacted turns.
- **"Offline" definition:** an owner is offline for a session when
  `now - last_at >= idleThreshold` (default 15 min, tunable). This guards against
  compacting an active conversation mid-flow, per "only compact if users offline."
  Because `idleThreshold` (15 min) < the session gap (1h), a session can be compacted
  while idle and still continue if the owner returns within the hour — they simply
  resume from the summary.
- **Compaction step:** for each eligible session, summarize `summary` + the raw turns
  after `summary_through_log_id` into a refreshed `summary` via a **haiku** call
  (cheap; reuse `knowClient`, not the sonnet dispatch client), then set
  `summary_through_log_id` to the latest turn and `summary_at = now`. Raw turns are no
  longer included in future prompts (the summary represents them) but remain in
  `dispatch_log` for audit.
- **Safety/robustness:** best-effort — a failed summarization is logged and retried next
  tick; the session keeps its raw tail until then. The compactor stops with the app
  context.

## Error handling

- Unset KB folder / vault → "Set it on the Dashboard first."
- File / note not found → clear, specific message.
- Path-traversal attempt → rejected by the guard.
- Oversized content → truncated (`…(truncated)`); Telegram-bound final replies capped
  at 3500 chars (model-context observations use the larger cap).
- Binary files → placeholder string, not raw bytes.
- Loop hits `maxSteps` without `reply` → return the last observation/status with a note
  that the task may be incomplete.
- Parse failure → existing tolerant fallback (treat raw output as the reply).

## Testing (TDD)

- **folderread:** temp-dir unit tests — list, read, traversal guard (`..` rejected),
  binary placeholder, truncation, missing dir.
- **obsidian Reader:** temp-vault tests — frontmatter parse, `[[wikilink]]` + `#tag`
  extraction, fuzzy note resolution, backlinks, text/tag search.
- **dispatch loop:** stub `Executor` + canned sequential sonnet JSON — assert the loop
  feeds observations back, chains read→send, terminates on `reply`, respects `maxSteps`,
  and that destructive actions still gate on confirmation.
- **session memory:** in-memory/temp store — gap > 1h starts a new session, ≤ 1h
  continues; prompt assembly uses summary + tail; `recall_memory` returns matching
  summaries.
- **compactor:** seed an idle session with raw turns + a stub summarizer — assert it
  compacts idle sessions, skips active ones (within `idleThreshold`), advances
  `summary_through_log_id`, and that a summarizer error leaves the tail intact.

## Implementation phasing

The work spans three coupled concerns in the dispatcher; suggested plan phases:

1. **Folder tools + sonnet:** `folderread`, `obsidian.Reader`, the 5 read actions,
   separate sonnet dispatch client. (Unblocks most of the acceptance test once paired
   with phase 2.)
2. **Multi-step loop:** `actionResult` refactor + bounded `Run` loop. (Completes the
   acceptance test.)
3. **Session memory + compaction:** `dispatch_sessions` schema, session-aware prompt
   assembly, `recall_memory` action, `SessionCompactor` cron.

## Out of scope

- Replacing the per-message `claude --print` subprocess spawn with a persistent,
  warm Claude **process** (a deeper latency optimization). Distinct from the
  conversation *session memory* in Section 6, which is about remembered context, not
  the process model. The multi-step loop already removes the "can't act" problem.
- Editing/writing into the KB folder (read-only by design; cowork remains the
  write surface).
- A dashboard setting for the dispatch model (chose hardcoded sonnet, not configurable).
- Backlink/link-graph caching (on-demand scan is fine for a personal vault).
- A dashboard UI for browsing/searching session summaries (recall is agent-only for now).
