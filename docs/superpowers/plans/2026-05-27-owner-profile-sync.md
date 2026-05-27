# Owner Profile Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Telegram dispatch bot a configurable role/persona ("owner profile") stored as one vault file (`Profile.md`), readable/writable from Cowork (MCP), Obsidian (direct edit), and the bot itself.

**Architecture:** A new `internal/ownerprofile` store owns read+write of `<vault>/Profile.md`. One store instance is shared by the dispatch executor (role injection + two new dispatch actions) and the HTTP server (a `/api/owner-profile` endpoint that the separate `--mcp` process calls). The dispatch bot reads the profile each turn and prepends it to its system prompt; the JSON action schema is unchanged.

**Tech Stack:** Go, standard library (`os`, `path/filepath`, `net/http`, `sync`), existing JSON-RPC MCP server.

---

## File Structure

- **Create** `internal/ownerprofile/ownerprofile.go` — the read/write store. One job: I/O for `Profile.md` at a fixed vault path.
- **Create** `internal/ownerprofile/ownerprofile_test.go` — store unit tests.
- **Modify** `internal/agent/dispatch.go` — two action consts, two `Executor` methods, two `execute` cases, catalog + system-prompt schema entries, role injection in `Run`.
- **Modify** `internal/agent/dispatch_test.go` — extend `fakeExec`, add role-injection + action tests.
- **Modify** `main.go` — `dispatchExecutor.ownerProfile` field + two methods; construct + inject the store; `srv.SetOwnerProfile`; seed `Profile.md` at startup.
- **Modify** `internal/server/server.go` — `Server.ownerProfile` field, `SetOwnerProfile` setter, route, `handleOwnerProfile` (GET + POST).
- **Modify** `internal/mcp/mcp.go` — two tool definitions, two `execute` cases, two handler methods.

Pattern references in the existing code: `cowork.Root` wiring (`main.go:839`, `server.go:45/126`), `get_cowork_folder` MCP tool (`mcp.go:585/592`), `dispatchExecutor.kbRoot` (`main.go:476`), the "adding a new action" recipe (`dispatch.go:13`).

---

## Task 1: `ownerprofile` store — Read

**Files:**
- Create: `internal/ownerprofile/ownerprofile.go`
- Test: `internal/ownerprofile/ownerprofile_test.go`

- [ ] **Step 1: Write the failing test**

```go
package ownerprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead_Missing(t *testing.T) {
	s := New(t.TempDir())
	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read missing: unexpected err %v", err)
	}
	if got != "" {
		t.Fatalf("Read missing: want empty, got %q", got)
	}
}

func TestRead_Present(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProfileFilename), []byte("hello dad"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello dad" {
		t.Fatalf("want %q got %q", "hello dad", got)
	}
}

func TestDisabled(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Fatal("empty path should be disabled")
	}
	if _, err := s.Read(); err == nil {
		t.Fatal("disabled Read should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ownerprofile/`
Expected: FAIL — `undefined: New` / `undefined: ProfileFilename`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package ownerprofile stores the owner's profile/persona document as a single
// Markdown file inside the Obsidian vault. It is the single source of truth for
// the dispatch bot's role, and is read/written by the bot, the MCP server
// (for the Cowork app), and edited directly in Obsidian.
package ownerprofile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProfileFilename is the fixed name of the profile note inside the vault.
const ProfileFilename = "Profile.md"

// ErrDisabled signals no vault directory is configured.
var ErrDisabled = errors.New("ownerprofile: no vault configured")

// Store reads and writes <vaultDir>/Profile.md. Zero-value / empty-dir Store is
// disabled — every method returns ErrDisabled, so callers can wire it
// unconditionally (mirrors folderread.Root).
type Store struct {
	dir string
	mu  sync.Mutex
}

// New returns a Store rooted at vaultDir. Empty vaultDir → disabled Store.
func New(vaultDir string) *Store {
	return &Store{dir: strings.TrimSpace(vaultDir)}
}

// Enabled reports whether a vault directory is configured.
func (s *Store) Enabled() bool { return s != nil && s.dir != "" }

func (s *Store) path() string { return filepath.Join(s.dir, ProfileFilename) }

// Read returns the profile contents. A missing file yields ("", nil).
func (s *Store) Read() (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ownerprofile/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ownerprofile/
git commit -m "feat(ownerprofile): add profile store with Read"
```

---

## Task 2: `ownerprofile` store — Write (replace/append, auto-create dir)

**Files:**
- Modify: `internal/ownerprofile/ownerprofile.go`
- Test: `internal/ownerprofile/ownerprofile_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestWrite_ReplaceCreatesFileAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Vault") // not yet created
	s := New(dir)
	if err := s.Write("first", "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "first" {
		t.Fatalf("want %q got %q", "first", got)
	}
	if err := s.Write("second", "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Read()
	if got != "second" {
		t.Fatalf("replace: want %q got %q", "second", got)
	}
}

func TestWrite_Append(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Write("line1", "replace"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("line2", "append"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "line1\nline2" {
		t.Fatalf("append: want %q got %q", "line1\nline2", got)
	}
}

func TestWrite_DefaultModeIsReplace(t *testing.T) {
	s := New(t.TempDir())
	_ = s.Write("a", "replace")
	if err := s.Write("b", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "b" {
		t.Fatalf("empty mode should replace: got %q", got)
	}
}

func TestWrite_Disabled(t *testing.T) {
	if err := New("").Write("x", "replace"); err == nil {
		t.Fatal("disabled Write should error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ownerprofile/`
Expected: FAIL — `s.Write undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `ownerprofile.go`:

```go
// Write stores content. mode "append" appends after a newline separator; any
// other value (including "" and "replace") overwrites. Creates the vault
// directory and file if missing.
func (s *Store) Write(content, mode string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	if mode == "append" {
		existing, err := os.ReadFile(s.path())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(existing) > 0 {
			content = string(existing) + "\n" + content
		}
	}
	return os.WriteFile(s.path(), []byte(content), 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/ownerprofile/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ownerprofile/
git commit -m "feat(ownerprofile): add Write with replace/append + dir auto-create"
```

---

## Task 3: Extend the dispatch `Executor` interface + test fake

**Files:**
- Modify: `internal/agent/dispatch.go:147-169` (the `Executor` interface)
- Modify: `internal/agent/dispatch_test.go:47-258` (the `fakeExec` struct + methods)

- [ ] **Step 1: Add the two methods to the `Executor` interface**

In `dispatch.go`, inside `type Executor interface { ... }`, after the `SearchNotes(...)` line, add:

```go
	GetOwnerProfile(ctx context.Context) (string, error)
	UpdateOwnerProfile(ctx context.Context, content, mode string) error
```

- [ ] **Step 2: Extend `fakeExec` in `dispatch_test.go`**

Add fields to the `fakeExec` struct (near line 47):

```go
	ownerProfile     string // returned by GetOwnerProfile
	ownerProfileErr  error  // returned by GetOwnerProfile when set
	updatedProfile   string // captured by UpdateOwnerProfile
	updatedProfileMd string // captured mode
```

Add methods (alongside the other `fakeExec` methods):

```go
func (f *fakeExec) GetOwnerProfile(ctx context.Context) (string, error) {
	return f.ownerProfile, f.ownerProfileErr
}

func (f *fakeExec) UpdateOwnerProfile(ctx context.Context, content, mode string) error {
	f.updatedProfile = content
	f.updatedProfileMd = mode
	return nil
}
```

- [ ] **Step 3: Run to verify the package compiles & existing tests pass**

Run: `go test ./internal/agent/`
Expected: PASS (no behavior change yet; interface + fake in sync).

- [ ] **Step 4: Commit**

```bash
git add internal/agent/dispatch.go internal/agent/dispatch_test.go
git commit -m "feat(dispatch): add owner-profile methods to Executor + fake"
```

---

## Task 4: Role injection — bot prepends the profile to its system prompt

**Files:**
- Modify: `internal/agent/dispatch.go:303-319` (`Run`: build system prompt before the loop)
- Test: `internal/agent/dispatch_test.go`

- [ ] **Step 1: Write the failing test**

The existing tests use a fake `ClaudeRunner` that records the system prompt. Add a test that asserts the profile is injected. (If the existing fake runner does not expose the system prompt, add a `gotSystem string` field to it and capture the first arg in `Reply`.)

```go
func TestRun_InjectsOwnerProfile(t *testing.T) {
	runner := &fakeRunner{reply: `{"action":"reply","user_reply":"hi"}`}
	ex := &fakeExec{ownerProfile: "I am Dad, an insurance agent. Be formal."}
	d := NewDispatcher(runner, ex, nil)

	d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})

	if !strings.Contains(runner.gotSystem, "I am Dad, an insurance agent") {
		t.Fatalf("system prompt missing owner profile; got:\n%s", runner.gotSystem)
	}
	if !strings.Contains(runner.gotSystem, "Respond with ONE JSON object only") {
		t.Fatalf("system prompt dropped the action schema")
	}
}

func TestRun_NoProfile_SystemPromptUnchanged(t *testing.T) {
	runner := &fakeRunner{reply: `{"action":"reply","user_reply":"hi"}`}
	ex := &fakeExec{} // empty ownerProfile
	d := NewDispatcher(runner, ex, nil)

	d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "hello"})

	if strings.Contains(runner.gotSystem, "OWNER PROFILE") {
		t.Fatalf("empty profile should not inject a profile block")
	}
}
```

Note: check the actual fake runner type name/fields in `dispatch_test.go` and adapt (`fakeRunner`/`gotSystem` may differ). If it lacks system-prompt capture, add:
```go
func (f *fakeRunner) Reply(ctx context.Context, system, conv string) (string, error) {
	f.gotSystem = system
	return f.reply, f.err
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRun_InjectsOwnerProfile`
Expected: FAIL — profile not in system prompt.

- [ ] **Step 3: Implement role injection in `Run`**

In `dispatch.go`, before the `for step := ...` loop in `Run`, compute the system prompt once. Replace the loop's `d.Claude.Reply(ctx, dispatchSystemPrompt, transcript)` call to use the computed prompt:

```go
	sysPrompt := d.ownerProfileSystemPrompt(ctx)
	// ...
	for step := 0; step < maxDispatchSteps; step++ {
		raw, err := d.Claude.Reply(ctx, sysPrompt, transcript)
```

Add the helper:

```go
// ownerProfileSystemPrompt returns dispatchSystemPrompt, optionally prefixed
// with the owner's profile so the bot adopts it as its persona. A missing or
// failed profile read degrades silently to the base prompt — the profile is
// context, never a hard dependency.
func (d *Dispatcher) ownerProfileSystemPrompt(ctx context.Context) string {
	if d.Exec == nil {
		return dispatchSystemPrompt
	}
	profile, err := d.Exec.GetOwnerProfile(ctx)
	if err != nil || strings.TrimSpace(profile) == "" {
		return dispatchSystemPrompt
	}
	return "OWNER PROFILE (your persona and context — honor it, but ALWAYS obey the OUTPUT SCHEMA below):\n" +
		strings.TrimSpace(profile) + "\n\n" + dispatchSystemPrompt
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestRun_`
Expected: PASS (both new tests + existing).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/
git commit -m "feat(dispatch): inject owner profile as the bot's role"
```

---

## Task 5: `get_owner_profile` / `update_owner_profile` dispatch actions

**Files:**
- Modify: `internal/agent/dispatch.go` — action consts (`:17-40`), `execute` switch, `actionCatalog` (`:284`), `dispatchSystemPrompt` schema (`:238` + action list `:251-274`)
- Test: `internal/agent/dispatch_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestRun_GetOwnerProfile(t *testing.T) {
	runner := &fakeRunner{reply: `{"action":"get_owner_profile","params":{},"user_reply":"Here it is:"}`}
	ex := &fakeExec{ownerProfile: "Dad, formal tone."}
	d := NewDispatcher(runner, ex, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "show my profile"})
	if res.Action != ActionGetOwnerProfile {
		t.Fatalf("action: got %v", res.Action)
	}
	if !strings.Contains(res.UserReply, "Dad, formal tone.") {
		t.Fatalf("reply missing profile: %q", res.UserReply)
	}
}

func TestRun_UpdateOwnerProfile(t *testing.T) {
	runner := &fakeRunner{reply: `{"action":"update_owner_profile","params":{"content":"New bio","mode":"replace"},"user_reply":"Saved."}`}
	ex := &fakeExec{}
	d := NewDispatcher(runner, ex, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "set my profile to New bio"})
	if res.Action != ActionUpdateOwnerProfile {
		t.Fatalf("action: got %v", res.Action)
	}
	if ex.updatedProfile != "New bio" || ex.updatedProfileMd != "replace" {
		t.Fatalf("not written: %q / %q", ex.updatedProfile, ex.updatedProfileMd)
	}
}

func TestRun_UpdateOwnerProfileRequiresContent(t *testing.T) {
	runner := &fakeRunner{reply: `{"action":"update_owner_profile","params":{"content":""},"user_reply":"ok"}`}
	d := NewDispatcher(runner, &fakeExec{}, nil)
	res := d.Run(context.Background(), DispatchInput{Channel: "telegram", OwnerID: "1", Message: "x"})
	if !strings.Contains(res.UserReply, "failed") {
		t.Fatalf("expected failure note, got %q", res.UserReply)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRun_.*OwnerProfile`
Expected: FAIL — `undefined: ActionGetOwnerProfile`.

- [ ] **Step 3: Implement the actions**

Add consts (in the `const ( ... )` action block):

```go
	ActionGetOwnerProfile    Action = "get_owner_profile"
	ActionUpdateOwnerProfile Action = "update_owner_profile"
```

Add cases in `execute`'s switch (before `default:`):

```go
	case ActionGetOwnerProfile:
		profile, err := d.Exec.GetOwnerProfile(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(profile) == "" {
			return "(no profile set yet)", nil
		}
		return "\n" + profile, nil

	case ActionUpdateOwnerProfile:
		var args struct {
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return "", fmt.Errorf("update_owner_profile params: %w", err)
		}
		if strings.TrimSpace(args.Content) == "" {
			return "", errors.New("update_owner_profile: content required")
		}
		if err := d.Exec.UpdateOwnerProfile(ctx, args.Content, args.Mode); err != nil {
			return "", err
		}
		mode := args.Mode
		if mode == "" {
			mode = "replace"
		}
		return "(profile " + mode + "d)", nil
```

Add to `actionCatalog` string (append before the closing backtick): `, get_owner_profile, update_owner_profile`.

Add to `dispatchSystemPrompt`: (a) the two names into the `"action":` enum line; (b) two bullet entries in the "Action params:" list:

```
- get_owner_profile: {} — return the owner's saved profile/persona document.
- update_owner_profile: {"content": "...", "mode": "replace" | "append"} — save the owner's profile (mode defaults to replace). Use when the owner asks to set/update their profile. To edit, first get_owner_profile (continue:true), merge, then update_owner_profile with the full new text.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/
git commit -m "feat(dispatch): add get_owner_profile + update_owner_profile actions"
```

---

## Task 6: Wire the store into `main.go` (executor + server + seed)

**Files:**
- Modify: `main.go` — `dispatchExecutor` struct + methods; construct store; inject into executor and server; seed `Profile.md`.

- [ ] **Step 1: Add the field + methods to `dispatchExecutor`**

Add an import for `claude-bridge/internal/ownerprofile` (match the existing import block style).

Add a field to the `dispatchExecutor` struct (next to `cowork`):

```go
	ownerProfile *ownerprofile.Store
```

Add the two methods near the other executor methods:

```go
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
```

- [ ] **Step 2: Construct + inject the store**

In the dispatcher-wiring block (`main.go:836-854`), after `coworkRoot := cowork.New(vaultPath)`:

```go
	ownerProfileStore := ownerprofile.New(vaultPath)
	seedProfileNote(vaultPath)
```

Add `ownerProfile: ownerProfileStore,` to the `&dispatchExecutor{...}` literal (alongside `cowork: coworkRoot,`).

After `srv.SetCowork(coworkRoot)` (around `main.go:872`), add:

```go
	srv.SetOwnerProfile(ownerProfileStore)
```

- [ ] **Step 3: Add the seed helper**

Add near `seedWelcomeNote` (`main.go:531`):

```go
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
```

- [ ] **Step 4: Verify it builds**

Run: `go build ./...`
Expected: success (no unused imports, signatures line up). `srv.SetOwnerProfile` is undefined until Task 7 — if building before Task 7, expect that one error and proceed to Task 7 first, or implement Tasks 6 & 7 together then build.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: wire owner-profile store into executor + seed Profile.md"
```

---

## Task 7: HTTP endpoint `/api/owner-profile` (GET + POST) on the server

**Files:**
- Modify: `internal/server/server.go` — struct field (`:45` area), `SetOwnerProfile` setter (`:123` area), route (`:366` area), handler.

- [ ] **Step 1: Add the field + setter**

Add an import for `claude-bridge/internal/ownerprofile`.

Add to the `Server` struct (next to `cowork *cowork.Root`):

```go
	ownerProfile *ownerprofile.Store // nil-safe via Enabled(); shared profile note
```

Add the setter near `SetCowork`:

```go
// SetOwnerProfile attaches the owner-profile store. Used by the
// /api/owner-profile endpoint (and the get_owner_profile / update_owner_profile
// MCP tools) so the Cowork app can read and update the shared profile.
func (s *Server) SetOwnerProfile(st *ownerprofile.Store) {
	s.ownerProfile = st
}
```

- [ ] **Step 2: Register the route**

Next to `mux.HandleFunc("/api/cowork/folder", s.handleCoworkFolder)` (`server.go:366`):

```go
	mux.HandleFunc("/api/owner-profile", s.handleOwnerProfile)
```

- [ ] **Step 3: Implement the handler**

Add near `handleCoworkFolder` (`server.go:503`). Mirror its `writeJSON` style:

```go
// handleOwnerProfile reads (GET) or writes (POST) the shared owner profile.
// POST body: {"content": "...", "mode": "replace"|"append"}.
func (s *Server) handleOwnerProfile(w http.ResponseWriter, r *http.Request) {
	if s.ownerProfile == nil || !s.ownerProfile.Enabled() {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "owner profile disabled — set an Obsidian vault path (or knowledge folder) first"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		content, err := s.ownerProfile.Read()
		if err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true, "content": content})
	case http.MethodPost:
		var body struct {
			Content string `json:"content"`
			Mode    string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "bad request body"})
			return
		}
		if strings.TrimSpace(body.Content) == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "error": "content required"})
			return
		}
		if err := s.ownerProfile.Write(body.Content, body.Mode); err != nil {
			writeJSON(w, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]interface{}{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
```

Ensure `encoding/json` and `strings` are imported in `server.go` (add if missing).

- [ ] **Step 4: Verify it builds + run server tests**

Run: `go build ./... && go test ./internal/server/`
Expected: build succeeds; server tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go
git commit -m "feat(server): add /api/owner-profile GET+POST endpoint"
```

---

## Task 8: MCP tools `get_owner_profile` / `update_owner_profile`

**Files:**
- Modify: `internal/mcp/mcp.go` — tool defs in `getTools()` (`:94-516`), `execute` switch (`:536`), two handler methods.

- [ ] **Step 1: Add tool definitions**

In `getTools()`'s returned slice, after the cowork tool, add:

```go
		{
			Name:        "get_owner_profile",
			Description: "Get the owner's shared profile/persona document. This is the same profile the Telegram dispatch bot uses as its role; call it at the start of a conversation to load the owner's context.",
			InputSchema: inputSchema{
				Type:       "object",
				Properties: map[string]interface{}{},
			},
		},
		{
			Name:        "update_owner_profile",
			Description: "Update the owner's shared profile/persona document. Use 'replace' (default) to overwrite with the full new text, or 'append' to add to it. This profile is read by the Telegram dispatch bot and editable in Obsidian.",
			InputSchema: inputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"content": map[string]interface{}{
						"type":        "string",
						"description": "The profile text to save.",
					},
					"mode": map[string]interface{}{
						"type":        "string",
						"description": "'replace' (default) overwrites; 'append' adds to the existing profile.",
					},
				},
				Required: []string{"content"},
			},
		},
```

- [ ] **Step 2: Add `execute` cases**

In `execute`'s switch, before `default:`:

```go
	// Owner profile
	case "get_owner_profile":
		return e.httpGet("/api/owner-profile")
	case "update_owner_profile":
		return e.updateOwnerProfile(args)
```

- [ ] **Step 3: Add the POST handler**

Add near `getCoworkFolder`. Mirror the existing POST style used by `sendMessage` (build JSON, `e.client.Post`):

```go
func (e *toolExecutor) updateOwnerProfile(args json.RawMessage) callToolResult {
	var params struct {
		Content string `json:"content"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	if strings.TrimSpace(params.Content) == "" {
		return errorResult("content is required")
	}
	body, _ := json.Marshal(params)
	resp, err := e.client.Post(e.baseURL+"/api/owner-profile", "application/json", bytes.NewReader(body))
	if err != nil {
		return errorResult(fmt.Sprintf("Cannot reach Claude Bridge at %s — is the dashboard running?\nError: %v", e.baseURL, err))
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return callToolResult{Content: []contentItem{{Type: "text", Text: string(out)}}}
}
```

(`bytes` is already imported in `mcp.go`; confirm.)

- [ ] **Step 4: Verify it builds**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/mcp.go
git commit -m "feat(mcp): add get_owner_profile + update_owner_profile tools"
```

---

## Task 9: Full build + test sweep

- [ ] **Step 1: Run everything**

Run: `go build ./... && go test ./...`
Expected: all green.

- [ ] **Step 2: Manual smoke (optional, requires running app)**

```bash
make restart
curl -s http://127.0.0.1:10002/api/owner-profile
curl -s -X POST http://127.0.0.1:10002/api/owner-profile -H 'Content-Type: application/json' -d '{"content":"Test profile","mode":"replace"}'
curl -s http://127.0.0.1:10002/api/owner-profile
```
Expected: first GET `{"ok":true,"content":"# Owner Profile..."}` (seeded), POST `{"ok":true}`, second GET shows `"Test profile"`.

- [ ] **Step 3: Final commit if anything changed**

```bash
git add -A && git commit -m "chore: owner-profile sync — build/test sweep"
```

---

## Notes for the implementer

- The MCP server (`claude-bridge --mcp`) is a **separate process** that talks to the running app over HTTP — that is why the MCP tools call `/api/owner-profile` rather than touching the store directly.
- The **one** manual setup step (not code): the owner adds a line to dad's Cowork project instructions — "At the start call `get_owner_profile`; when I change my profile call `update_owner_profile`." Document this in `README`/`SETUP.md` if convenient, but it is out of scope for these tasks.
- Verify exact fake type names in `dispatch_test.go` (`fakeRunner`, `fakeExec`) before writing Task 4/5 tests; adapt field names if they differ.
