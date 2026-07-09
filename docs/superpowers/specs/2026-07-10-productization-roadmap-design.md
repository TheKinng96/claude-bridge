# Productization Roadmap — Insurance-Agent Assistant

**Date:** 2026-07-10
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

### Phase 2 — Insurance value
- Policy data model: `policies` table (client_id, policy_no, insurer,
  product type, premium, renewal_date, status) + CRUD dispatch actions +
  dashboard page.
- Persistent scheduler (SQLite-backed): renewal reminders, birthdays,
  follow-ups → notifies owner, drafts client message for approval.
- Structured policy extraction from uploaded documents (extends existing
  classifier).

### Phase 3 — Onboarding + UI + i18n
- First-run wizard: API key → Telegram bot (guided BotFather flow) →
  WhatsApp link → owner profile.
- Unified design system: single nav, single settings model, consistent
  pages.
- i18n framework, EN / 中文 / Bahasa Melayu.
- Rebrand: new product name, README/site rewrite as insurance-agent
  assistant.

### Phase 4 — WhatsApp Business Cloud API + relay
- Hosted webhook relay; local app holds outbound connection.
- Template messages and compliant broadcasts via Cloud API.
- whatsmeow demoted to "personal mode (advanced)" flag.

### Phase 5 — Distribution + licensing
- License server on the thin cloud; one-time keys; offline-tolerant
  validation.
- Signed + notarized installers (macOS first, Windows next); official
  release repo; auto-update from signed feed.
- Opt-in error reporting; data backup/export.

**Ordering logic:** Phase 1 unblocks everything; Phase 2 creates the
sellable story; Phase 3 makes it sellable to strangers; Phase 4 makes the
channel legal; Phase 5 lets it scale.

## 6. Out of scope (this roadmap)

- Multi-tenancy / hosted SaaS.
- Facebook/Instagram/LinkedIn/XHS connectors (existing FB code untouched).
- Voice/video message intake.
- Claims processing, quotes, needs-analysis tooling (candidate Phase 6+).

## 7. Open questions (to resolve in phase designs)

- Product name (Phase 3 rebrand).
- Which insurers' document formats to support first for policy extraction
  (Phase 2) — start with the ones in the owner's KB.
- Relay hosting choice + cost model (Phase 4) — must stay cheap enough for
  one-time pricing.
- Windows support timing (Phase 5).
