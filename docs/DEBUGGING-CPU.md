# Debugging high CPU (the "400% CPU" guide)

A practical, ordered playbook for tracking down a CPU spike in `claude-bridge`.
Works on macOS. Follow the steps in order — each one narrows the search.

> **Key idea:** CleanMyMac (and Activity Monitor's "energy" view) often group a
> process under the **app that launched it**. So "iTerm is using 400%" usually
> means *a child process of the shell running inside iTerm* — most likely
> `claude-bridge` itself, a `node`/`claude` CLI subprocess it spawned, or a
> headless `Chromium`. Step 1 tells you which.

---

## Step 1 — Identify the real process

In the Claude Code session, the `!` prefix runs a command and drops the output
right here:

```
! ps -Ao pcpu,pid,ppid,comm -r | head -15
```

`pcpu` = %CPU (100 = one full core; 400 = four cores). `comm` = the binary.
Read the top line. Then match it:

| Top process (`comm`) | What it means | Go to |
|---|---|---|
| `claude-bridge` | The Go app itself is spinning | **Step 3** |
| `node` or `claude` | A spawned Claude CLI subprocess | **Step 4** |
| `Google Chrome` / `Chromium` / `* Helper` | The headless browser engine | **Step 5** |
| `iTerm2` | The terminal rendering a flood of log output | **Step 6** |

Also note **PID** and **PPID** (parent). If the hog's PPID is the
`claude-bridge` PID, the app spawned it.

### Is it sustained or transient?

Sample twice, ~30s apart:

```
! ps -Ao pcpu,pid,comm -r | head -5 ; sleep 30 ; echo --- ; ps -Ao pcpu,pid,comm -r | head -5
```

If the spike **disappears** after a minute, it was almost certainly the one-time
Chromium download (~150MB) on first launch — not a bug. If it's **sustained**,
keep going.

---

## Step 2 — Note the configuration

Two things change CPU behavior; record them before profiling:

- **Knowledge folder size.** A freshly-pointed folder with many files triggers a
  classification pass (one `claude` CLI call per file, rate-limited). Check:
  `! find "<your knowledge folder>" -type f | wc -l`
- Whether **Telegram / WhatsApp / Facebook** are connected (each runs a poller).

---

## Step 3 — Profile the Go app (`claude-bridge`)

The app can serve Go's `pprof` profiler, but **only when started with `--pprof`**
(off by default, localhost-only). Restart it like this:

```
./claude-bridge --pprof localhost:6060
```

(Add `--no-tray` if you run it headless.) Reproduce the high CPU, then:

### 3a. Full goroutine dump (what every goroutine is doing)

```
! curl -s http://localhost:6060/debug/pprof/goroutine?debug=2 > /tmp/goroutines.txt ; wc -l /tmp/goroutines.txt ; grep -c '^goroutine ' /tmp/goroutines.txt
```

Then skim it: a *spinning* goroutine shows the same stack on repeated dumps and
is usually `running` rather than blocked on `chan receive` / `select` / `IO wait`.
Look for a stack stuck in app code (e.g. `internal/...`). Paste the suspicious
stack here.

### 3b. 30-second CPU profile (where the time actually goes)

```
! go tool pprof -top -seconds 30 http://localhost:6060/debug/pprof/profile
```

The top rows are the hottest functions. `go tool pprof` is part of the Go
toolchain you already have. For a visual: drop `-top` and run `web` inside the
interactive prompt (needs Graphviz), or `-svg > /tmp/cpu.svg`.

### 3c. Quick goroutine count over time (leak/storm detector)

```
! for i in 1 2 3; do curl -s http://localhost:6060/debug/pprof/goroutine?debug=1 | head -1; sleep 5; done
```

A steadily *climbing* count = a goroutine leak (something spawns and never exits).

---

## Step 4 — Claude CLI subprocesses (`node` / `claude`)

The app shells out to the `claude` CLI (a Node process) for: knowledge
classification, WhatsApp auto-reply, Telegram dispatch, profile extraction, and
session compaction. Each call is one short-lived `claude --print`.

```
# How many are alive right now, and their parents:
! pgrep -fl 'claude --print' ; echo "count:" ; pgrep -f 'claude --print' | wc -l
```

- **Many at once / constantly respawning** → something is calling the agent in a
  loop. Check the app's log output for a repeating line (classification of the
  same file, a reply retry, etc.).
- **One stuck at high CPU for a long time** → a single `claude` invocation hung.
  Note its PID; `sample <pid> 3` (macOS) prints what it's doing.
- Expected: brief spikes during a knowledge-folder scan, then idle.

---

## Step 5 — Headless browser (`Chromium` / `Chrome`)

Only runs when a browser connector (Facebook/Instagram) is active, or briefly at
first launch while it downloads/verifies Chromium.

```
! pgrep -fl -i 'chromium\|Google Chrome' | head
```

If a headless Chromium is pinned high while you're not using Facebook, that's the
culprit — it shouldn't be running at idle. Capture the command line (the `pgrep
-fl` output) and paste it.

---

## Step 6 — Log flood (`iTerm2` itself is the hog)

If `iTerm2` is genuinely the top process, the app is printing log lines fast
enough that the terminal can't keep up rendering. Confirm by redirecting logs to
a file and watching the rate:

```
./claude-bridge --no-tray > /tmp/cb.log 2>&1 &
! sleep 5 ; wc -l /tmp/cb.log ; sleep 5 ; wc -l /tmp/cb.log
```

If the line count jumps by hundreds in 5s, find the repeating message:

```
! sort /tmp/cb.log | uniq -c | sort -rn | head
```

The top line is what's spamming. Paste it — that's the loop to fix. (A file sink
also instantly fixes the *symptom*: iTerm stops rendering the flood.)

---

## What to send back

Whichever step you reach, paste:
1. The Step 1 `ps` line (the actual hog + its PPID).
2. Sustained vs transient.
3. The relevant artifact: a goroutine stack (3a), the pprof `-top` (3b), the
   subprocess list (4/5), or the top repeating log line (6).

That pins the root cause to one component — then it's a targeted fix, not a guess.

---

## Notes

- `--pprof` is **opt-in** and binds to **localhost only**. Leave it off in normal
  use; turn it on just for a debugging session.
- `go tool pprof` ships with the Go toolchain (`go version` to confirm).
- macOS extras: `sample <pid> 5 -file /tmp/s.txt` profiles *any* process
  (including `node`/Chromium) for 5s without special flags; Activity Monitor →
  double-click a process → **Sample** does the same with a GUI.
