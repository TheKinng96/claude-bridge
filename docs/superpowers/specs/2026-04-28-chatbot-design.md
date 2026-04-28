# WhatsApp Chatbot — Contact Groups, SOP Modes & Review UI

Date: 2026-04-28

## Overview

Expand the WhatsApp auto-reply agent with contact group management, configurable reply SOP per group, a pending-reply review UI, and magic-link remote access. The goal is for the dad (sole operator) to control which contacts get auto-replied, which need his review first, and which are ignored — all without touching code.

---

## Data Model

### New tables

```sql
CREATE TABLE contacts (
  id           INTEGER PRIMARY KEY,
  jid          TEXT NOT NULL UNIQUE,   -- e.g. "60123456789@s.whatsapp.net"
  platform     TEXT NOT NULL DEFAULT 'whatsapp',
  push_name    TEXT,
  first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE groups (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL,
  type         TEXT NOT NULL CHECK(type IN ('manual','auto')),
  reply_mode   TEXT NOT NULL CHECK(reply_mode IN ('auto','review','off')),
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE contact_groups (
  contact_id   INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  group_id     INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  source       TEXT NOT NULL CHECK(source IN ('manual','auto')),
  assigned_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (contact_id, group_id)
);

CREATE TABLE pending_replies (
  id            INTEGER PRIMARY KEY,
  contact_jid   TEXT NOT NULL,
  platform      TEXT NOT NULL DEFAULT 'whatsapp',
  incoming_msg  TEXT NOT NULL,
  proposed_reply TEXT NOT NULL,
  status        TEXT NOT NULL CHECK(status IN ('pending','approved','rejected','sent'))
                DEFAULT 'pending',
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  reviewed_at   DATETIME
);

CREATE TABLE magic_tokens (
  id          INTEGER PRIMARY KEY,
  token_hash  TEXT NOT NULL UNIQUE,   -- SHA-256 of the raw UUID token
  expires_at  DATETIME NOT NULL,
  used_at     DATETIME
);
```

### Existing tables (unchanged)
- `credentials` — global agent settings (reply_mode, system_prompt, flow_steps, model)
- `cached_messages` — conversation history per contact

---

## Reply Mode Resolution

When a message arrives, effective reply_mode is resolved in priority order:

1. Manual group assignment — highest priority (source of truth)
2. Auto group assignment
3. Global setting from credentials (fallback)

If a contact belongs to multiple groups, the first manual group wins; if none, first auto group wins.

---

## Reply Pipeline

```
incoming WhatsApp message
        ↓
upsert contact in contacts table (on first message)
        ↓
resolve effective reply_mode
        ↓
mode = off     → ignore, do nothing
mode = auto    → generate reply → send immediately (existing flow)
mode = review  → generate reply → insert into pending_replies (status=pending)
               → WhatsApp notify dad (batched, max once per 5 min if new pending exist):
                 "You have N pending replies. Review: http://x.x.x.x:10002/messages"
```

---

## Auto-Group Sync Job

- Frequency: configurable (daily / weekly), stored in agent config
- Reads contract data — data source defined separately (CSV drop or external API)
- Updates `contact_groups` rows where `source='auto'` only
- Never touches `source='manual'` assignments
- Logs last sync timestamp, shown in dashboard
- Manual [Run Now] trigger available in agent config page

---

## Messages Tab (Review UI)

New dashboard tab: **Messages**

### Pending sub-tab
```
[ ] Select all                               [Approve All] [Reject All]

[ ] John Tan · 2 min ago
    Incoming:  "Hi, I'm interested in medical insurance"
    Reply:     "Hi John! I'm happy to help. Could you share your full name..."
    [Approve]  [Edit & Send]  [Reject]
```

- **Approve** → send reply via WhatsApp, status → `sent`
- **Edit & Send** → inline text edit, then send
- **Reject** → discard, status → `rejected`, no message sent
- Bulk approve/reject via checkboxes

### Sent sub-tab
History of all approved/sent replies with contact name, message, timestamp.

---

## Contact & Group Management UI

New dashboard tab: **Contacts**

### Groups view (default)
```
Global reply mode: [Auto ▾]   ← tri-state: all-on / partial / all-off

[+ New Group]
Name          | Type   | Contacts | Reply Mode
VIP Clients   | Manual | 12       | [Auto ▾]
Prospects     | Manual | 34       | [Review ▾]
Renewal Due   | Auto   | 8        | [Auto ▾]    last sync: 2h ago
Lapsed        | Auto   | 5        | [Off ▾]
```

- Click group → contacts in group, add/remove contacts from manual groups
- Auto groups: show last sync time, read-only membership

### Contacts view
```
Search...
John Tan   +60123456789   [VIP Clients] [Renewal Due]   Last: 2h ago
Mary Lim   +60198765432   [Prospects]                   Last: 1d ago
(new)      +60111234567   —                             Last: 5 min ago
```

- Click contact → conversation history + group assignment panel (checkboxes for manual groups only)

---

## Magic Link Auth

Remote access without a password:

```
Dad sends "!login" from his own WhatsApp number
    ↓
App detects command (checks sender JID matches a configured "owner JID")
Generates UUID token, stores SHA-256 hash in magic_tokens (30 min TTL, single-use)
Replies via WhatsApp:
  "Dashboard login (30 min): http://x.x.x.x:10002/auth?token=<uuid>"
    ↓
Dad opens link
GET /auth?token=<uuid>
  → hash token, look up in magic_tokens
  → valid + not expired + not used → mark used_at, set session cookie → redirect /
  → invalid/expired → "Link expired. Send !login again."
    ↓
Session cookie: HttpOnly, SameSite=Strict, 24h TTL
All dashboard routes require valid session, redirect /login if missing
/login page: shows "Send !login via WhatsApp to get a login link"
```

Security notes:
- Only owner JID can trigger token generation
- Token is single-use and 30-min expiry
- Session cookie is HttpOnly + SameSite=Strict
- Tailscale encrypts the tunnel for remote access; no additional TLS required

---

## Out of Scope (this iteration)

- Per-contact reply mode override (group-level is enough for now)
- Multi-user / role-based access
- Auto-group job business logic (depends on contract data format, defined separately)
- Facebook/Instagram equivalent (WhatsApp only for now)
