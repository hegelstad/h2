# Design Doc: Crush Harness for h2

Status: REVISED after review — incorporates `crush-harness-review.md`
(reviewer-ox, 2026-08-23) in full; §1–3 reworked, review §4/§5 folded in as
decisions. 2026-08-23
Owner: coder-ox · Reviewer: concierge
Related: `docs/plans/crush-harness-review.md` (the review this revision
incorporates), `docs/plans/opencode-harness.md` (template + opencode
comparison), `internal/session/session.go` (child lifecycle),
`internal/session/message/delivery.go` (input path),
`internal/session/agent/harness/{claude,codex,generic,grok}`

## 1. Summary

Add **Crush** (Charmbracelet, Go — same lineage as opencode) as an h2 harness as
the lightweight replacement for the opencode/Bun track (~600 MB RSS/agent).
Verified on this box with the Ox Alpha stealth model via OpenRouter:

| Question | Answer |
|---|---|
| **(a) Does Ox Alpha work via Crush?** | **Yes.** `openrouter/stealth/ox-alpha` is in Crush's catalog; base_url `https://openrouter.ai/api/v1`; key from `OPENROUTER_API_KEY` env. Text turns, tool-calling (client-executed tools), multi-turn (`--session <id>`) all verified end-to-end. |
| **(b) Memory (harness process only; h2 daemon+VT overhead excluded)** | `crush run` one-shot turn: ~26 MB at start → **peak ~90 MB** during a turn. `crush server`: **~64 MB idle**. Nothing lingers after run-mode exit. vs opencode ~600 MB → **4 agents ≈ 350–400 MB total** harness-side. |
| **(c) Turn-done mechanism** | **Exec/one-shot semantics**: `crush run "<prompt>"` exits when the turn completes — exit 0 = success, ≠0 = error (stderr actionable), final text on stdout (`-q` = clean). A *real process exit* — stronger than opencode's `session.idle` event. Transport into h2 is the **turn-supervisor shim** (§3), not exec-as-PTY-child (§2). |
| **(d) Isolation** | Verified at two levels: per-agent `XDG_CONFIG_HOME`/`XDG_DATA_HOME` (disjoint dirs see `[]` sessions), **and same-cwd concurrency via per-agent `--data-dir`** — verified: two agents, same workspace, no `.crush` created in cwd, separate session dbs, concurrent turns OK (§4.1). |

Install: Crush v0.91.0 single static Go binary from GitHub releases, at
`/usr/local/bin/crush`. Pin this version; startup version-gate required (§6.4).

## 2. The one decision that matters: who owns the child process?

h2 sessions own **exactly one** long-lived child, started once via
`VT.StartPTY` (`internal/session/session.go:519`, `:591`). When that child
exits, `lifecycleLoop` marks `ChildExited`, calls `Queue.Pause()`, and waits
for a *user* relaunch (`session.go:620-661`) — no auto-respawn. The Harness
interface (`harness/harness.go:69`) only supplies args/env for that one child.

Consequences, stated plainly:

- **`crush run` cannot be the PTY child.** Its exit after turn 1 ends the whole
  h2 session and pauses delivery — the opposite of the design goal.
- The earlier claim that this "maps to codex/generic" was **wrong**: codex is
  one long-lived TUI driven by OTEL events (`codex/harness.go:199`); generic is
  one long-lived process driven by output-silence (`generic/harness.go:76`).
  Neither respawns children.
- **Option A (native exec transport)** — respawn-per-message in
  Session/lifecycleLoop, non-TTY delivery via the `InputSender` seam
  (`harness/harness.go:107-136`, whose own comment forbids speculative wiring)
  — is **out of scope for v1**. It touches the live delivery hot path.

**Decision (v1): Option B — turn-supervisor shim.** The PTY child is a small
long-lived supervisor (go:embed source, built into h2). It reads delivered
lines from stdin (existing `deliver()` typing works unchanged), and per line:
emits Active via hook plumbing → spawns one `crush run` → tees stdout → on
exit emits the completion hook → Idle. Zero h2-core changes; live `h2 peek`
transcript for free; claude-pattern hook robustness; the 2-min watchdog
(`monitor/monitor.go:179` `maybeReconcileIdle`) still backstops a missed
event. Option A remains a tracked follow-up, not a v1 dependency.

## 3. Architecture

```mermaid
flowchart TD
  subgraph h2proc["h2 session process"]
    H["CrushHarness\n(internal/.../harness/crush)"]
    MON["AgentMonitor (Active/Idle)"]
    HH["h2 handle-hook\n(handle_hook.go:52-60)"]
    L["listener.go:52 hook_event →\n:232 Session.HandleHookEvent"]
    H -- "state transitions" --> MON
    HH --> L --> H
  end

  subgraph child["PTY child (one, long-lived): h2-crush-supervisor"]
    S["supervisor loop\n(stdin lines = turns)"]
    C["crush run -q -D <agent data-dir>\n[--session <id>] <line>\n(one process per turn)"]
    S -->|"spawn / wait / tee stdout→PTY"| C
  end

  S -- "h2 handle-hook --agent $H2_ACTOR\n{\"hook_event_name\":\"crush.turn.started\"}" --> HH
  C -- "exit 0 → crush.turn.completed\nexit ≠0 → crush.turn.failed (+stderr tail)" --> S
```

Per-turn data flow:
1. h2 delivers a message → existing PTY writer → supervisor reads one line.
2. Supervisor emits `crush.turn.started` → harness → **Active**.
3. Supervisor spawns `crush run -q -D <data-dir> [--session <id>] "<line>"`,
   tees stdout to its own stdout → **`h2 peek` shows a live plain-text
   transcript + scrollback**; stderr is captured for error mapping (§6.3).
4. On exit: `crush.turn.completed` (0) or `crush.turn.failed` (≠0, stderr tail)
   → harness → **Idle** → h2 releases the next queued message.

No screen scraping anywhere; no changes to Session/delivery/lifecycle hot
paths. Human interactivity survives via PTY passthrough (typing a line = one
turn).

## 4. Verified configuration (all findings reproduced on v0.91.0)

**Gotcha (max_tokens/402):** Crush's catalog default `max_tokens` for ox-alpha
is 64000. OpenRouter rejects with HTTP 402 when the credit balance cannot cover
worst-case cost (*"You requested up to 64000 tokens, but can only afford
1826"*) — reproduced consistently. Fix verified — pin output tokens in the
model slots:

`$XDG_CONFIG_HOME/crush/crush.json` (**minimal working config**; in the
harness, large/small `max_tokens` become **role-configurable template vars** —
see §6.2):
```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "global_context_paths": ["/abs/per-agent/CRUSH.md"],
    "disable_provider_auto_update": true
  },
  "models": {
    "large": { "provider": "openrouter", "model": "stealth/ox-alpha", "max_tokens": 4096 },
    "small": { "provider": "openrouter", "model": "stealth/ox-alpha", "max_tokens": 2048 }
  }
}
```

- No provider block needed: `openrouter` is a known provider; auth from
  `OPENROUTER_API_KEY` env (config supports literal `"$VAR"` expansion — never
  write the real key to disk).
- Tool auto-approval: `--yolo` is **not accepted by `run`** in v0.91.0. Use
  `"permissions": { "allowed_tools": ["bash","edit","write","view","glob",
  "grep","ls","multiedit","patch","read","todowrite","webfetch"] }` (verified).
- Upstream docs on `main` already diverge from v0.91.0 CLI — treat
  https://charm.land/crush.json as source of truth for the pinned version.

### 4.1 Isolation — same-cwd deployment shape (VERIFIED)

Review hole: `<project>/.crush/crush.db` is cwd-local, so 4 agents on one
workspace would share one SQLite db (collisions + writer contention). Verified
fix: **pass `-D/--data-dir <per-agent dir>` on every command** (`run`, `session
list`, everything). Test result (two agents, same cwd, concurrent):
- both turns exit 0;
- **no `.crush/` created in the shared cwd** — `--data-dir` relocates the
  project db entirely;
- each agent's `crush session list --json -D <dir>` sees only its own sessions.

Config root renamed to something neutral: `~/h2home/harness-config/<agent>/`
(`xdg-config/`, `data/`) — not `opencode-config/`.

Socket insurance: always pass an explicit per-agent
`--host unix://<per-agent path>` so a stray `crush server` can never silently
collect runs via the box-wide default socket (`/tmp/<uid>/crush-<uid>.sock`).

### 4.2 Role instructions / system prompt (VERIFIED)

`crush run` has no system-prompt flag and the schema has no system-prompt
option (only provider-level `system_prompt_prefix`). Verified mechanism:
**per-agent context file** — EnsureConfigDir writes the role prompt to
`~/h2home/harness-config/<agent>/CRUSH.md` and points
`options.global_context_paths` at that absolute path. Test: role instruction
("every reply must end with PINEAPPLE") placed in that file was followed by
`crush run` in a shared workspace — role identity reaches the model without
touching shared files. Project-level `CRUSH.md` in the workspace is **not**
used for roles (it would leak across agents).

### 4.3 Session strategy (VERIFIED)

Without `--session`, **every `crush run` creates a new session** (observed).
Strategy per review §4(c): explicit `--session <id>`, id captured after the
first turn by diffing `crush session list --json -D <dir>` before/after —
verified reliable (exactly one new id; resume with full context confirmed).
Shim persists the id as `HarnessSessionID` via `EventSessionStarted`
(`monitor.go:490` path); `SupportsResume()=true` maps to
`--session <ResumeSessionID>`. Fallback if capture ever flakes: `--continue`,
unambiguous only because of per-agent `--data-dir`.

## 5. Supervisor shim spec

- **Stdin protocol:** one delivered message = one line = one turn (matches
  `deliver()`'s typed-line + `\r` shape; multi-line bodies arrive as
  file-reference paths per existing inline-vs-file logic — supervisor reads the
  file and passes contents as the prompt argv/stdin).
- **Hooks:** envelope `{"hook_event_name":"crush.turn.started|completed|failed"}`
  to `h2 handle-hook --agent $H2_ACTOR` (`handle_hook.go:52-60` requires the
  field). Routing already generic: `listener.go:52` → `:232`
  `Session.HandleHookEvent` → harness.
- **Interrupt (Ctrl+C = 0x03):** supervisor SIGINTs/kills the in-flight `crush
  run`, emits `crush.turn.failed` → Idle. No stranded Active.
- **Turn timeout:** configurable supervisor-level kill + `turn.failed` so a
  hung crush can't wedge an agent.
- **Error mapping:** on exit ≠0, pattern-match stderr — 402/payment →
  `EventServerErrorInfo`, auth → `EventAuthErrorInfo`, rate-limit → server
  info (`monitor.go:384-397`) — so the status bar shows the cause, not a silent
  Idle.
- **Version gate:** `PrepareForLaunch` execs `crush --version`, requires
  ≥ v0.91.0, actionable error otherwise (no existing gate to copy; pre-1.0 tool
  with diverging upstream docs makes this mandatory). Config sets
  `disable_provider_auto_update: true` (auto-update analogue of opencode's
  `OPENCODE_DISABLE_AUTOUPDATE=1`).
- **Metrics/log expectations (state, so nobody debugs a non-bug):** no OTEL
  from crush → `EventsReceived=false` in status bar is expected; no
  NativeLogPathSuffix — state is SQLite (`crush.db`), not tailable files.

## 6. Testing

Unit (in-package, `go test ./internal/session/agent/harness/crush/...`, rolls
into `make test`): BuildCommandArgs (fresh/resume/`--session`/`-D`/`--host`),
EnvVars (XDG + key), EnsureConfigDir idempotency with `setupFakeHome(t)`
(never touch real config per repo AGENTS.md), **permissions-drift test**
(generated crush.json parses + contains expected `allowed_tools` — catches
silent tool renames at version bumps), version-gate test, stderr→event mapping
tests.
Shim tests (stub `crush` fixture): exit 0 / exit 1 / SIGINT / timeout →
correct hook sequence and Idle transitions; hook-envelope shape asserted
against `handle_hook.go`'s required `hook_event_name`.
Integration (`tests/external`, Makefile-skipped in CI): **two agents, same
cwd, distinct `--data-dir`** (the deployment shape — the disjoint-dirs case is
the easy one, already verified in the spike); queued `h2 send` delivered only
after `turn.completed`; relaunch resumes via persisted `HarnessSessionID`.
Manual QA: real Ox Alpha tool-use turn through `h2 run --role crush-coder` +
`h2 send`, `h2 peek` transcript check.

## 7. Roles

`roles/crush-coder.yaml`: `harness: crush`, `model:
openrouter/stealth/ox-alpha`, role-configurable `maxTokensLarge/Small`
(default 4096/2048, comment explaining the OpenRouter 402 worst-case-cost
rejection so nobody "fixes" it back to 64000), system prompt delivered via the
CRUSH.md mechanism (§4.2). Role prompt should instruct the agent to put large
artifacts in files, not chat (4096 output cap can truncate long turns).

## 8. Build, deploy, rollout

Work in `/home/ubuntu/h2home/projects/workspace/h2` (origin `hegelstad/h2`),
branch `feat/crush-harness` (design doc already pushed, commit 067eeeb).
1. Implement per §3/§5: new package `harness/crush` + embedded supervisor +
   one role yaml; blank import beside `session.go:31-34`.
2. `make check` + `make test` green before each commit; `go build
   -buildvcs=false`; install to `~/go/bin/h2`; restart bridge + agents.
3. PR-first per repo convention; never merge feat/* to main directly.
Rollback: additive — revert the blank import; zero blast radius on
claude/grok/codex/generic (opencode remains design-doc-only today).

## 9. Decisions (was §7 open questions — closed by review)

| Question | Decision |
|---|---|
| Transport | Exec-per-turn **semantics** via PTY **supervisor shim** (§2 Option B). Peek preserved via stdout tee. Native exec transport (Option A / `InputSender`) deferred. |
| Replies/stdout | **No stdout→h2-message pipe.** Replies stay agent-initiated `h2 send` (driven by role prompt), harness-agnostic. Stdout = display (peek/scrollback); optional nice-to-have: persist per-turn text to event store (codex session-log-tail analogue). |
| Session strategy | Explicit `--session <id>`; capture after first turn via `session list --json` diff (verified §4.3); `--continue` only as fallback, safe under per-agent `--data-dir`. |
| Version pinning | Yes — new startup gate (§5), `disable_provider_auto_update` in config. |
