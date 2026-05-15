# WhatsApp Broadcast

## What it does

`batch_whatsapp_messages` submits a list of recipients to the Claude Bridge app, which loops through them in the background. Between each send the bridge waits a tier-based random delay (see table below) to mimic human pacing. When `personalize=true`, Claude rephrases the rendered message per contact using their name and recent inbound messages. A live browser page streams progress via Server-Sent Events. Two companion tools (`get_batch_status`, `cancel_batch`) let you query or abort a running batch programmatically.

---

## MCP tool reference

### `batch_whatsapp_messages`

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `recipients` | array of objects | yes | Each object must have `phone` (E.164). Optional keys: `contact_name`, `from_jid`. |
| `message` | string | yes | Base message template. Supports `{{name}}` and `{{push_name}}` placeholders. |
| `personalize` | boolean | no | If `true`, Claude rephrases the rendered message per contact. Default `false`. |
| `instructions` | string | no | Tone/style guidance for personalization (e.g. `"warm and informal"`). Ignored when `personalize=false`. |
| `min_delay_seconds` | integer | no | Override minimum inter-send delay. Default: tier-based. Setting below 15 s greatly raises ban risk. |
| `max_delay_seconds` | integer | no | Override maximum inter-send delay. Default: tier-based. |

Returns `batch_id` immediately. The batch runs in the background.

**Example invocation**

```json
{
  "recipients": [
    {"phone": "+60123456789", "contact_name": "Ali"},
    {"phone": "+60198765432", "contact_name": "Siti"}
  ],
  "message": "Hi {{name}}, just a reminder about tomorrow's appointment.",
  "personalize": true,
  "instructions": "warm and conversational"
}
```

### `get_batch_status`

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `batch_id` | string | yes | ID returned by `batch_whatsapp_messages`. |

### `cancel_batch`

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `batch_id` | string | yes | ID of the running batch to cancel. |

Already-completed jobs stay completed; pending jobs are cancelled.

---

## Template variables

`{{name}}` and `{{push_name}}` are currently aliases — both expand to the contact's `contact_name` value.

If a placeholder key is not found in the variable map it is left as-is (e.g. `{{nme}}` stays `{{nme}}`). This makes typos visible in the sent message rather than silently disappearing.

---

## Tier-based pacing

Each recipient is classified into a tier based on their message history. The tier determines the default random delay applied before that send.

| Tier | Definition | Default delay |
|------|------------|---------------|
| Active | Contact replied to you within the last 30 days | 30–60 s |
| Quiet | Contact is saved but has not replied recently | 60–120 s |
| New | No chat history with this contact at all | 120–300 s |

When a batch contains mixed tiers, the slowest tier's delay range applies to the entire batch. This is conservative by design.

Delays can be overridden with `min_delay_seconds` / `max_delay_seconds`. Setting `min_delay_seconds` below 15 dramatically raises ban risk regardless of tier.

---

## Daily cap

| Threshold | Behaviour |
|-----------|-----------|
| 80 sends/day (soft) | Batch is submitted; response includes a `warning` field noting elevated detection risk. |
| 200 sends/day (hard) | Batch is rejected with HTTP 429 and `{"ok": false, "error": "daily cap exceeded (N/200) — wait until tomorrow or reduce batch size"}`. |

The counter is in-memory and keyed by local date string (`YYYY-MM-DD`). It resets automatically at midnight when the date changes. It resets to zero on app restart (see Known limitations).

The counter is incremented before the batch is submitted. If a submit fails after the count was bumped, the count is not rolled back (intentional — prevents race-past-cap on retry).

The daily cap is global across all WhatsApp accounts connected to the same app instance.

---

## Live progress page

URL pattern: `http://127.0.0.1:10002/broadcasts/{batch_id}`

The page is returned by the `batch_id` field in the tool response. It features:

- Progress bar (percentage complete)
- Counters: sent, failed, total, current status
- Activity log (per-job success/failure lines)
- Cancel button (calls `cancel_batch` server-side)
- Auto-disconnects when the batch completes or is cancelled

The page uses Server-Sent Events (`/api/batch/events?batch_id={id}`). It sends an initial snapshot of the current batch state before streaming live updates, so reloading mid-batch works correctly.

---

## Personalization

When `personalize=true`:

1. The message template is rendered with `{{name}}` / `{{push_name}}` substituted.
2. The rendered text, the contact's name, and their most recent inbound messages are sent to Claude with the instructions prompt.
3. Claude returns a tailored rephrase; that text is sent instead of the rendered template.

**Fallback behaviour:** if Claude fails for any reason (timeout, rate limit, API error, empty reply), the rendered template is sent unchanged. The send is never skipped because personalization failed. Each fallback is logged as `[personalize] Claude error for "Name", using template fallback: <reason>`.

The `instructions` parameter controls Claude's tone and style. Omitting it applies a conservative default: keep meaning, one short message, no greetings beyond a name, no emoji unless the original had one.

---

## Cancel & status

- **Progress page Cancel button** — cancels the batch from the browser.
- `cancel_batch` MCP tool — cancels programmatically; use `batch_id` from the original call.
- `get_batch_status` MCP tool — returns counts of pending/running/completed/failed jobs and the overall batch status.

---

## Safety caveats

> Read before sending to more than a handful of contacts.

- **Unofficial protocol.** This feature is built on whatsmeow, which implements the unofficial WhatsApp Web protocol. WhatsApp (Meta) may flag or ban accounts that send bulk messages, even at conservative pacing. There is no safe bulk-send path through the unofficial protocol.

- **Saved contacts are not immune.** Burst patterns, identical message text across many recipients, recipient report/block actions, and sending to contacts who have never started a conversation with you all increase account suspension risk.

- **The only Meta-sanctioned path for bulk send is the WhatsApp Business API.** That is a separate integration (Track B / Phase 2) not included in this build. If you need guaranteed deliverability and Meta compliance, use the Business API.

- **Setting `min_delay_seconds` below 15 dramatically raises ban risk** regardless of tier classification.

- **`personalize=true` reduces identical-text detection risk** because each message is individually rephrased, but it is not a guarantee against detection or banning.

---

## Known limitations

- **In-memory daily counter resets on app restart.** A restart clears the count; you could exceed 200 sends per calendar day across restarts.
- **Batch queue is in-memory.** App restart loses all pending and running batches. Any in-flight jobs at time of restart are not retried.
- **1-to-1 only.** Sending to WhatsApp group JIDs is not supported.
- **WhatsApp Business API not yet supported.** Track B / Phase 2 integration is out of scope for this build.
- **Daily cap is global per app instance.** It is not partitioned by WhatsApp account or connected device.

---

*Implementation plan: [`docs/superpowers/plans/2026-05-01-whatsapp-broadcast-loop.md`](superpowers/plans/2026-05-01-whatsapp-broadcast-loop.md)*
