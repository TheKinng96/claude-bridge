# Productization Roadmap — Insurance-Agent Assistant

**Date:** 2026-07-10 (revised 2026-07-16: P2/P3 reshaped around WhatsApp
scheduling + dashboard-first senior-friendly UI; insurance schema deferred)
**Status:** Approved (audit + roadmap); Phase 1 detailed design to follow
**Supersedes:** positioning in README.md ("Claude Bridge" multi-connector framing)

## 1. Goal

Turn the working single-tenant dispatch agent (built for one owner, "dad", an
insurance agent) into a sellable product for other insurance agents in SEA.

## 2. Decisions made

| Decision | Choice |
|---|---|
| Distribution | Local-first app + thin hosted cloud (license server, WA webhook relay, signed update feed). Not full SaaS. |
| AI access | Bring-your-own Anthropic API key, entered in dashboard. Replaces the `claude` CLI subprocess dependency. |
| Client messaging | WhatsApp Business Cloud API (official). whatsmeow personal mode retained behind an "advanced / at your own risk" flag so the existing owner install keeps working. |
| Pricing | One-time license key. (Consistent with local-first; full SaaS would have forced subscription.) |
| Target market | Insurance agents, SEA, multilingual (EN / 中文 / Bahasa Melayu), WhatsApp-first. Non-technical users assumed. |
| Product identity | WhatsApp scheduling + automation center. The owner's real usage pattern — manually spreading a few sends per day to avoid Meta spam detection — becomes the automated core feature. |
| Primary surface | Dashboard-first: web app is the main workspace (compose, calendar, contacts); Telegram/WhatsApp chat is the remote control. UI designed for non-technical users in their 50s. |
| Validation | Owner ("dad") is the pilot user throughout; new features land on his machine first and his needs feed the backlog. Insurance-domain features deferred until he asks. |

## 3. Audit summary (what blocks selling today)

### Structural walls
1. **Claude CLI dependency** — `internal/claude/client.go` shells out to
   `claude --print` for the dispatcher, document classifier, broadcast
   personalizer, and image vision. No API-key path exists; every customer
   would need an authenticated Claude Code install.
2. **Unofficial WhatsApp** — whatsmeow protocol; documented ban risk
   (docs/BROADCAST.md). Also: WA Business Cloud API requires a public
   webhook URL for inbound — a local-only app cannot receive it without a
   hosted relay. This makes the thin cloud component unavoidable.
3. **Single-owner architecture** — one global owner profile, one Telegram
   allowlist, one process. Acceptable for local-first; documented constraint.

### Security (pre-revenue blockers)
- Entire JSON API (`/api/*`, `/mcp/*`) unauthenticated: `auth.go:115-138`
  trusts all localhost and bypasses API routes. Local malware or DNS-rebinding
  from a webpage can drive the full API (send messages, read client PII).
- Secrets (Telegram bot token, Meta app secret) plaintext in SQLite.
- API handlers return raw `err.Error()` internals.
- Self-update pulls unsigned binaries from a personal GitHub repo
  (`updater.go:16`, `repo = "TheKinng96/claude-bridge"`) with quarantine
  stripped by the installer.

### Product basics missing
- No license enforcement; no onboarding wizard (user must know @BotFather,
  @userinfobot, QR flow, terminal); two inconsistent navbars; agent config
  written from three different UI surfaces; dashboard English-only;
  broadcast queue and daily-send-cap counter in-memory (cap can be exceeded
  across restarts); no backup/export; no error reporting for support;
  branding says "Claude Bridge" generic connector hub.

### Insurance value gap
- No insurance data model at all: no policies, renewals, premiums, claims,
  beneficiaries. "Insurance" exists only as prompt copy and doc-type tags.
- No scheduler of any kind — no renewal reminders, birthdays, follow-ups.
  This is the killer feature agents pay for.
- Real existing value: multi-account WhatsApp broadcast with pacing,
  Claude-classified knowledge base (FTS5 + optional Ollama vectors), passive
  client-profile extraction, human-in-the-loop reply review, natural-language
  Telegram dispatch with cross-session memory, Obsidian/cowork folder I/O.

## 4. Approach chosen

**Local-first + thin cloud.** Keep the single-tenant local binary. Add a
small hosted service (operated by us) providing: license-key validation,
WhatsApp Business webhook relay (app connects outbound — no customer
port-forwarding), and a signed update feed. Least rework, preserves the
privacy-first story, matches one-time-license pricing.

Rejected: full SaaS rebuild (months of tenancy rework across ~19 packages,
conflicts with one-time pricing). White-glove manual installs remain possible
during development but are not on the roadmap.

## 5. Roadmap

### Phase 1 — Engine + security foundation
- Direct Anthropic API client (BYO key) replacing all `claude` CLI call
  sites: dispatcher, classifier, personalizer, vision. Key entered and
  validated in dashboard settings.
- Authentication on all `/api/*` and `/mcp/*` routes (session token;
  closes localhost-trust / DNS-rebinding hole).
- Secrets encrypted at rest (OS-keychain-backed key).
- API error responses sanitized (no raw `err.Error()`).
- Broadcast queue + daily-send-cap counter persisted (crash-safe limits).

### Phase 2 — Scheduling + automation engine (core product value)
- Persistent scheduler (SQLite-backed, survives restart) with one queue
  for all outgoing content: WhatsApp messages, broadcasts, Facebook posts.
- Schedule types:
  - One-off scheduled sends ("send to Alice tomorrow 9am", broadcast Friday).
  - Recurring greetings: client birthdays (add DOB to `client_profiles`)
    and SEA festival calendar (CNY, Hari Raya, Deepavali, Christmas).
  - Follow-up sequences: "no reply in N days → nudge"; prospect drips.
- Human-pace spreading (automates what the owner does by hand today):
  daily send caps, quiet hours, randomized jitter, queue spread across the
  day to avoid Meta spam detection. Extends existing broadcast pacing into
  the core engine.
- Approval-first: auto-drafted content lands in the existing
  human-in-the-loop review queue unless the rule is explicitly auto-send.
- New dispatch actions so chat can create/list/cancel schedules.

### Phase 3 — Dashboard-first UI redesign + onboarding + i18n
- Main workspace screens: **Today** (going out today + needs approval),
  **Compose** (message → contacts/groups → channel WA/FB → send now or
  schedule), **Calendar** (month view of scheduled content), **Contacts**,
  **Settings**.
- Facebook posting becomes a channel toggle inside the same Compose flow
  (fixes current confusing separate surface).
- Compose content sources: AI draft, saved templates, and a linked Google
  Sheet — owner manually picks the latest image + message row (sheet linked
  in Settings; shares the Google OAuth groundwork with crma-0c9 Drive sync).
- Group management via a member-picker modal: tapping a group chip lists
  all clients in it with large checkboxes; the same modal creates new
  groups and drives "pick individually" in Compose.
- Senior-friendly design language (users in their 50s): large type, high
  contrast, one primary action per screen, generous tap targets, no jargon.
- Single design system, single nav, single settings model.
- First-run wizard: API key → Telegram bot (guided BotFather flow) →
  WhatsApp link → owner profile.
- i18n framework, EN / 中文 / Bahasa Melayu.
- Rebrand: new product name, README/site rewrite.

### Phase 4 — Cloud relay: WhatsApp Business API + remote MCP
- Hosted relay; local app holds outbound connection (no customer
  port-forwarding).
- WhatsApp Business Cloud API: webhook receipt via relay, template
  messages, compliant broadcasts.
- whatsmeow demoted to "personal mode (advanced)" flag.
- **Remote MCP connector**: relay exposes the app's tools as a remote MCP
  endpoint (streamable HTTP, OAuth 2.0 + PKCE — authless prohibited).
  Owner adds it as a custom connector on claude.ai once; it then works in
  Claude mobile (iOS/Android), web, desktop, and Cowork — phone chat
  becomes the remote control, and the owner's existing Cowork desktop
  workflow drives the same tools. Verified against Anthropic connector
  docs 2026-07-16 (mobile support confirmed; relay must be public HTTPS;
  Anthropic egress 160.79.104.0/21). Telegram becomes an optional legacy
  channel rather than the primary remote interface.

### Phase 5 — Distribution + licensing
- License server on the thin cloud; one-time keys; offline-tolerant
  validation.
- Signed + notarized installers (macOS first, Windows next); official
  release repo; auto-update from signed feed.
- Opt-in error reporting; data backup/export.

**Ordering logic:** Phase 1 unblocks everything (scheduler needs the API
client and persistence groundwork); Phase 2 creates the sellable story;
Phase 3 makes it sellable to strangers; Phase 4 makes the channel legal;
Phase 5 lets it scale.

## 6. Deferred / out of scope (this roadmap)

- **Insurance policy schema** (policies table, renewal tracking, structured
  policy extraction) — deferred until pilot-user validation shows demand.
  Renewal reminders can ride the Phase 2 scheduler later; the engine is
  built generic for this reason. Tracked as a backlog issue.
- Multi-tenancy / hosted SaaS.
- New connectors: Instagram/LinkedIn/XHS (existing FB code stays).
- Voice/video message intake.
- Claims processing, quotes, needs-analysis tooling.

## 7. Open questions (to resolve in phase designs)

- Product name (Phase 3 rebrand; mockup uses working name "Hantar").
- Festival calendar source + which festivals per market (Phase 2).
- Google Sheet column convention for campaign content (image, message,
  date added) — confirm with the sheet the owner actually uses (Phase 3).
- Follow-up sequences need reply detection — define "replied" for WhatsApp
  chats reliably (Phase 2).
- Relay hosting choice + cost model (Phase 4) — must stay cheap enough for
  one-time pricing.
- Windows support timing (Phase 5).
