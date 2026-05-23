# Dispatch Multi-Step Loop — Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let the dispatcher chain actions in one instruction (e.g. read a KB file, then WhatsApp a contact about it) by running a bounded multi-step loop, while keeping the existing one-shot behavior intact.

**Architecture:** Add an opt-in `"continue": true` flag to the dispatch JSON. When absent/false the dispatcher behaves exactly as today (execute one action, append its result to the reply, done). When true, the action runs, its result is fed back into the prompt, and the model issues the next action — looping up to `maxDispatchSteps` (5) or until it emits `reply`. This preserves backward compatibility (every existing test passes unchanged) and adds chaining.

**Tech Stack:** Go 1.25, module `claude-bridge`. Spec: `docs/superpowers/specs/2026-05-23-telegram-folder-access-design.md` (§1). Phase 1 (folder tools + sonnet) is already merged.

**Why opt-in continue (not an always-on loop):** an always-on loop would make the existing single-reply test fakes repeat the same action until the step cap, and would stop appending action results to `user_reply` — breaking ~20 existing dispatch tests. The `continue` flag defaults to the current behavior, so only NEW chaining tests are needed.

---

### Task 1: Add `continue` flag + sequenced test fake

**Files:**
- Modify: `internal/agent/dispatch.go` (the `dispatchPayload` struct, ~line 281)
- Modify: `internal/agent/dispatch_test.go` (`fakeClaude` ~line 12, add `newTestDispatcherSeq`)

- [ ] **Step 1: Write the failing test**

```go
// append to internal/agent/dispatch_test.go
func TestParseDispatchContinueFlag(t *testing.T) {
	p, err := parseDispatch(`{"action":"read_kb","params":{"path":"x.md"},"user_reply":"reading","continue":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Continue {
		t.Fatal("expected Continue=true")
	}
	p2, _ := parseDispatch(`{"action":"reply","params":{},"user_reply":"hi"}`)
	if p2.Continue {
		t.Fatal("expected Continue=false when omitted")
	}
}

func TestFakeClaudeSequence(t *testing.T) {
	c := &fakeClaude{replies: []string{"a", "b"}}
	if got, _ := c.Reply(context.Background(), "", ""); got != "a" {
		t.Fatalf("call 1 = %q, want a", got)
	}
	if got, _ := c.Reply(context.Background(), "", ""); got != "b" {
		t.Fatalf("call 2 = %q, want b", got)
	}
	// Past the end, repeats the last entry (avoids index panic).
	if got, _ := c.Reply(context.Background(), "", ""); got != "b" {
		t.Fatalf("call 3 = %q, want b (repeat last)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestParseDispatchContinueFlag|TestFakeClaudeSequence'`
Expected: FAIL — `p.Continue` undefined / `fakeClaude` has no `replies` field.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/dispatch.go`, add the `Continue` field to `dispatchPayload`:

```go
// dispatchPayload is the parsed JSON Claude returns.
type dispatchPayload struct {
	Action    Action          `json:"action"`
	Params    json.RawMessage `json:"params"`
	UserReply string          `json:"user_reply"`
	Continue  bool            `json:"continue"` // true → run this action, feed result back, expect a follow-up action
}
```

In `internal/agent/dispatch_test.go`, extend `fakeClaude` and add the helper:

```go
type fakeClaude struct {
	reply   string   // single-reply mode (back-compat)
	replies []string // sequence mode; one per Reply call, repeats last past the end
	calls   int
	err     error
	lastS   string
	lastU   string
}

func (f *fakeClaude) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastS = system
	f.lastU = user
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	if len(f.replies) > 0 {
		i := f.calls - 1
		if i >= len(f.replies) {
			i = len(f.replies) - 1
		}
		return f.replies[i], nil
	}
	return f.reply, nil
}

// newTestDispatcherSeq builds a dispatcher whose fake Claude returns the given
// replies in order (repeating the last past the end). Use for multi-step tests.
func newTestDispatcherSeq(replies ...string) (*Dispatcher, *fakeClaude, *fakeExec, *fakeStore) {
	c := &fakeClaude{replies: replies}
	ex := &fakeExec{}
	st := &fakeStore{}
	return NewDispatcher(c, ex, st), c, ex, st
}
```

Note: the existing `newTestDispatcher(reply string)` (single-reply) is unchanged and still used by all current tests.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -run 'TestParseDispatchContinueFlag|TestFakeClaudeSequence'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(dispatch): add continue flag to payload + sequenced test fake"
```

---

### Task 2: Rework `Run` into a bounded continue-loop

**Files:**
- Modify: `internal/agent/dispatch.go` (`Run`, ~lines 263-318; add step constants near `memoryWindow` ~line 258)
- Modify: `internal/agent/dispatch_test.go` (new chaining tests)

- [ ] **Step 1: Write the failing tests**

```go
// append to internal/agent/dispatch_test.go
func TestRunChainsReadThenSend(t *testing.T) {
	// Step 1: read_kb with continue:true. Step 2: send_whatsapp (terminal).
	d, c, ex, _ := newTestDispatcherSeq(
		`{"action":"read_kb","params":{"path":"report.md"},"user_reply":"reading","continue":true}`,
		`{"action":"send_whatsapp","params":{"phone":"60123","message":"Q1 is up"},"user_reply":"Messaged Alice."}`,
	)
	ex.kbContent = "Q1 revenue up 12%."
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read report.md and tell 60123"})

	// The read_kb result must have been fed back into the prompt for step 2.
	if !strings.Contains(c.lastU, "Q1 revenue up 12%") {
		t.Fatalf("read result not fed back into prompt; lastU=%q", c.lastU)
	}
	// The send actually happened.
	if len(ex.sendCalls) != 1 {
		t.Fatalf("want 1 send, got %d", len(ex.sendCalls))
	}
	// Final reply is the last action's message, with the action trail appended.
	if !strings.Contains(res.UserReply, "Messaged Alice") {
		t.Fatalf("unexpected final reply %q", res.UserReply)
	}
	if !strings.Contains(res.UserReply, "read_kb → send_whatsapp") {
		t.Fatalf("expected action trail in reply, got %q", res.UserReply)
	}
	// Two Claude calls (one per step).
	if c.calls != 2 {
		t.Fatalf("want 2 Claude calls, got %d", c.calls)
	}
}

func TestRunReplyTerminatesImmediately(t *testing.T) {
	d, c, _, _ := newTestDispatcherSeq(`{"action":"reply","params":{},"user_reply":"Hey!"}`)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hi"})
	if res.Action != ActionReply || res.UserReply != "Hey!" {
		t.Fatalf("unexpected %s / %q", res.Action, res.UserReply)
	}
	if strings.Contains(res.UserReply, "→") {
		t.Fatalf("single reply should have no trail: %q", res.UserReply)
	}
	if c.calls != 1 {
		t.Fatalf("want 1 call, got %d", c.calls)
	}
}

func TestRunSingleActionOneShotUnchanged(t *testing.T) {
	// No continue flag → behaves one-shot: status appended, no trail, one call.
	d, c, ex, _ := newTestDispatcher(`{"action":"read_kb","params":{"path":"x.md"},"user_reply":"Here:"}`)
	ex.kbContent = "hello world"
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "read x.md"})
	if res.Action != ActionReadKB {
		t.Fatalf("want read_kb, got %s", res.Action)
	}
	if !strings.Contains(res.UserReply, "hello world") {
		t.Fatalf("one-shot read should append content, got %q", res.UserReply)
	}
	if strings.Contains(res.UserReply, "→") {
		t.Fatalf("single action should have no trail: %q", res.UserReply)
	}
	if c.calls != 1 {
		t.Fatalf("want 1 call, got %d", c.calls)
	}
}

func TestRunStepCapStops(t *testing.T) {
	// Model always asks to continue with a read; loop must stop at maxDispatchSteps.
	d, c, ex, _ := newTestDispatcherSeq(
		`{"action":"read_kb","params":{"path":"a.md"},"user_reply":"...","continue":true}`,
	)
	ex.kbContent = "x"
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "loop"})
	if c.calls != maxDispatchSteps {
		t.Fatalf("want %d calls (step cap), got %d", maxDispatchSteps, c.calls)
	}
	if res.UserReply == "" {
		t.Fatal("expected a non-empty final reply at the cap")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestRun`
Expected: FAIL — chaining not implemented (`maxDispatchSteps` undefined; chaining tests fail because current `Run` only does one step and never feeds results back).

- [ ] **Step 3: Write the implementation**

Add step constants near `memoryWindow` in `internal/agent/dispatch.go`:

```go
const (
	maxDispatchSteps = 5    // bound on chained actions per owner message
	observationLimit = 6000 // chars of an action result fed back into the prompt
)
```

Replace the entire `Run` method (currently ~lines 263-318, from `func (d *Dispatcher) Run` through its closing brace) with:

```go
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
		d.logAsync(ctx, in, res, time.Since(start))
		return res
	}

	recent := d.recentTurns(ctx, in)
	transcript := buildDispatchUserPrompt(in, recent)
	var trail []string

	for step := 0; step < maxDispatchSteps; step++ {
		raw, err := d.Claude.Reply(ctx, dispatchSystemPrompt, transcript)
		if err != nil {
			res.Error = err.Error()
			res.UserReply = "Sorry — dispatch failed: " + truncate(err.Error(), 200)
			d.logAsync(ctx, in, res, time.Since(start))
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
			d.logAsync(ctx, in, res, time.Since(start))
			return res
		}

		status, exErr := d.execute(ctx, parsed)
		trail = append(trail, string(parsed.Action))
		res.Action = parsed.Action

		// Chain only when the model asked to continue AND a step remains.
		// Otherwise behave one-shot: append this action's result and stop.
		if !parsed.Continue || step == maxDispatchSteps-1 {
			if exErr != nil {
				res.Error = exErr.Error()
				res.UserReply = parsed.UserReply + " (failed: " + truncate(exErr.Error(), 100) + ")"
			} else if status != "" {
				res.UserReply = strings.TrimSpace(parsed.UserReply + " " + status)
			} else {
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

	d.logAsync(ctx, in, res, time.Since(start))
	return res
}
```

Confirm `fmt` is imported in dispatch.go (it is — used elsewhere in the file).

- [ ] **Step 4: Run the full agent test suite**

Run: `go test ./internal/agent/`
Expected: PASS — the 4 new TestRun tests AND every pre-existing dispatch test (the one-shot path is unchanged for `continue:false`).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(dispatch): bounded multi-step continue-loop with action trail"
```

---

### Task 3: Teach the system prompt to chain

**Files:**
- Modify: `internal/agent/dispatch.go` (`dispatchSystemPrompt`, ~lines 177-250)
- Modify: `internal/agent/dispatch_test.go` (prompt assertion)

- [ ] **Step 1: Write the failing test**

```go
// append to internal/agent/dispatch_test.go
func TestSystemPromptDocumentsContinue(t *testing.T) {
	if !strings.Contains(dispatchSystemPrompt, `"continue"`) {
		t.Fatal("system prompt must document the continue flag")
	}
	if !strings.Contains(dispatchSystemPrompt, "read_kb") {
		t.Fatal("system prompt should still list the actions")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestSystemPromptDocumentsContinue`
Expected: FAIL — prompt doesn't mention `"continue"` yet.

- [ ] **Step 3: Edit the system prompt**

In `dispatchSystemPrompt`, change the JSON schema block to include the `continue` field. Replace:

```
{
  "action": "send_whatsapp" | "broadcast_whatsapp" | "search_kb" | "list_pending" | "summary_inbox" | "list_cowork" | "read_cowork" | "search_cowork" | "edit_cowork" | "reply",
  "params": { ... },
  "user_reply": "Short status to send back to the owner (1-2 sentences)."
}
```

with:

```
{
  "action": "send_whatsapp" | "broadcast_whatsapp" | "search_kb" | "list_pending" | "summary_inbox" | "list_contacts" | "get_profile" | "update_profile" | "extract_profile" | "list_cowork" | "read_cowork" | "search_cowork" | "edit_cowork" | "cowork_path" | "list_kb" | "read_kb" | "read_note" | "backlinks" | "search_notes" | "reply",
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
```

Then replace Rule 3:

```
3. Keep user_reply short. The executor will append a status line ("Sent." / "3 results.") after your reply.
```

with:

```
3. Keep user_reply short. For a single action (continue false), the action's result is appended to your user_reply automatically. For a continue:true chain, intermediate user_reply text is NOT sent — only your final "reply" message reaches the owner.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/`
Expected: PASS (prompt test + all others).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "docs(dispatch): document continue/chaining in the system prompt"
```

---

### Task 4: Full build + manual smoke (the acceptance test)

**Files:** none (verification).

- [ ] **Step 1: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: SUCCESS; all packages PASS.

- [ ] **Step 2: Live acceptance test (requires running app + Telegram bot + a KB folder)**

Start the app, set a knowledge-base folder containing a short `.md` file (e.g. `report.md`), and have at least one WhatsApp contact. Message the bot something like:

> "Read report.md from my knowledge folder and send 60XXXXXXXXX a one-line summary."

Confirm: the bot reads the file (you can watch logs for `read_kb`), then sends the WhatsApp message, and replies with a short confirmation ending in a `· read_kb → send_whatsapp` trail. This is the owner's Phase-2 acceptance test.

- [ ] **Step 3: Commit (if any prompt tweaks were needed from the smoke test)**

```bash
git add -A && git commit -m "chore(dispatch): phase-2 prompt tweaks from smoke test"
```

(Skip if no changes. Use explicit file paths if you do commit — do not `git add -A` unrelated files.)

---

## Self-Review Notes

- **Spec coverage (§1):** bounded loop with `maxDispatchSteps` (Task 2) ✓; result fed back into prompt (Task 2, `observationLimit`) ✓; `reply` terminates (Task 2) ✓; only final message sent + action trail (Task 2) ✓; chaining documented for the model (Task 3) ✓; destructive >20-recipient guard preserved (unchanged Rule 1 in the prompt) ✓.
- **Deviation from spec:** the spec described an always-on loop with an `actionResult{observation,userStatus}` refactor. This plan instead uses an opt-in `continue` flag and reuses the existing `execute` status as the observation. Rationale: backward compatibility (no rewrite of ~20 existing tests, one-shot behavior preserved) and far less churn, while delivering the same chaining capability. The `observation` is the existing status string (capped at `observationLimit` when fed back); read actions already return their content as that status.
- **Placeholder scan:** none — every step has complete code.
- **Type consistency:** `dispatchPayload.Continue` (Task 1) is read in `Run` (Task 2); `maxDispatchSteps`/`observationLimit` defined in Task 2 and used there + asserted in Task 2 tests; `fakeClaude.replies`/`calls` (Task 1) used by Task 2 tests; `newTestDispatcherSeq` (Task 1) used in Task 2.
- **Backward-compat guarantee:** `continue:false`/omitted preserves the exact prior `Run` behavior (single execute, status appended, no trail), so existing tests are untouched.
