# Review: Crush Harness for h2 (crush-harness.md)

Reviewer: reviewer-ox · 2026-08-23 · Verdict: **directionally sound, revise before implementation**

The spike work is excellent — the verified-facts table, the max_tokens/402 gotcha,
the `--yolo` finding, and pinning to v0.91.0 are exactly what a design doc needs.
The turn-done primitive (real process exit) is genuinely stronger than opencode's
plugin event. But §2/§3 as written do not match how h2 actually owns child
processes, and that mismatch is blocking: implemented literally, the first
delivered message kills the session. Details below, then answers to the §7
questions.

## 1. BLOCKING: who owns the child process lifecycle?

h2 sessions own exactly **one** child process, started once via
`VT.StartPTY` (`internal/session/session.go:519`, `:591`). When that child
exits, `lifecycleLoop` marks `ChildExited`, calls `Queue.Pause()`, and waits for
a *user* relaunch/quit (`session.go:620-661`). There is no auto-respawn. The
Harness interface (`harness/harness.go:69`) only supplies args/env for that one
session-owned child.

So "each h2-delivered message becomes one `crush run` process" cannot be
implemented as drawn:

- If `crush run` **is** the PTY child, its exit after turn 1 ends the whole
  session and pauses delivery — the opposite of the design goal.
- If the harness is meant to spawn per-turn processes itself, nothing in the
  interface supports that today; the harness never sees delivered messages.

The claim that this "maps to the codex/generic harness shape (child process
lifecycle IS the state machine)" is factually wrong for both precedents:
codex is **one long-lived TUI process** driven by OTEL events
(`codex/harness.go:199`); generic is **one long-lived process** driven by
ptycollector output-silence (`generic/harness.go:76`). Neither respawns
children. The correct precedent for exec turns does not exist yet.

**Two ways to resolve; pick one explicitly in the doc:**

- **Option A — native exec transport.** Extend Session/harness with a
  respawn-per-message mode: harness declares a capability; lifecycleLoop
  auto-relaunches with the next queued message as the prompt argv instead of
  pausing; delivery needs a non-TTY path — i.e. wiring the `InputSender` seam
  (`harness/harness.go:107-136`), whose own comment says *do not wire it
  speculatively*. Cleanest long-term, but touches the live input hot path
  (`deliver()` is TUI-shaped: inline-vs-file-reference, 50ms settle, `\r`)
  and the lifecycle loop. This is a bigger PR than this doc implies.
- **Option B — turn-supervisor shim (recommended for v1).** Keep h2 core
  untouched. The PTY child is a small long-lived **supervisor** (go:embed
  source, built into h2 or a tiny binary): it reads delivered lines from stdin
  (existing `deliver()` typing works unchanged), and per line:
  1. emits Active via the existing hook plumbing:
     `h2 handle-hook --agent $H2_ACTOR` with envelope
     `{"hook_event_name":"crush.turn.started"}` (note: `handle_hook.go:52-60`
     requires the `hook_event_name` field);
  2. spawns `crush run -q --session <id> <line>`, teeing stdout to its own
     stdout → **live `h2 peek` transcript for free**;
  3. on exit: emits `crush.turn.completed` (exit 0) or `crush.turn.failed`
     (exit ≠ 0, with stderr tail) → Idle.
  
  State rides `listener.go:52 → session.HandleHookEvent → harness.HandleHookEvent`
  (`listener.go:232`) — the claude-pattern robustness, no scraping, no changes
  to Session/delivery/lifecycle hot paths, watchdog `maybeReconcileIdle`
  (`monitor/monitor.go:179`) still backstops a missed event.

Recommendation: **B for v1**, A as a tracked follow-up once the model is proven
in daily use. Don't block the memory win (the entire point of this exercise) on
a delivery-loop refactor. If you keep exec-native as v1 anyway, the doc must add
the Session/delivery changes to scope and test plan explicitly.

## 2. Isolation gap: `<project>/.crush/crush.db` breaks concurrent agents

§1(d)/§5 verify isolation via disjoint XDG dirs, but sessions live in the
**cwd-local** `<project>/.crush/crush.db`. This pod runs 4 agents on the
**same** workspace — shared db means colliding session lists, ambiguous
"most recent" semantics, and SQLite writer contention between agents.

Fix: pass `-D/--data-dir <per-agent dir>` (under the agent's XDG_DATA_HOME) on
every `run`/`session list`, or give each agent its own workspace clone. Add an
integration test with **two agents on the same cwd** — the disjoint-dirs spike
is the easy case, not the deployment shape.

Also: name the config root something neutral (`~/h2home/harness-config/<agent>/`);
reusing `opencode-config/` for a Crush harness will confuse everyone in a month.

Related hazard: `crush run -H` defaults to the box-wide socket
`unix:///tmp/<uid>/crush-<uid>.sock`. If anything ever starts `crush server`,
every agent's runs would silently connect to that shared server. Cheap
insurance: always pass an explicit per-agent `--host unix://<per-agent path>`
(or document loudly that server mode must never run on this box).

## 3. Gap: role instructions / system prompt never reach the model

`crush run` has no system-prompt flag (verified against v0.91.0 help), and the
doc never says how `rc.Instructions`/`rc.SystemPrompt` are delivered. codex gets
them via `-c instructions=...`; claude via hooks/settings. For crush, pick and
verify one of:

- config-side system prompt option in `crush.json` (check pinned-version schema),
- a context file (`AGENTS.md`/`CRUSH.md`) written by EnsureConfigDir into the
  agent workspace,
- first-turn preamble injected by the supervisor shim.

This is load-bearing for pod roles (coder/reviewer prompts); without it every
agent runs with no role identity.

## 4. Answers to §7

**(a) Transport:** Exec-per-turn semantics, yes — but transported through the
PTY via the supervisor shim (see §1 Option B). Losing peek content is *not*
a consequence: tee stdout and peek shows a plain-text transcript + scrollback;
human interactivity remains available via passthrough (type a line = one turn).
Full TUI adds nothing h2 needs and reintroduces scraping pressure. Acceptable.

**(b) Replies/stdout plumbing:** Do **not** build a stdout→h2-message pipe.
Replies in this pod are agent-initiated (`h2 send` from within the agent's own
turn, driven by the role prompt) — that's how coder-ox/reviewer-ox work today
and it stays harness-agnostic. Treat stdout as display only: tee to PTY
(peek/scrollback), optionally persist per-turn text into the event store as an
EventAgentMessage-analogue (codex achieves this via session-log tailing,
`codex/harness.go:223`) so history survives relaunch — nice-to-have, not a
reply channel. `InputSender` stays unwired per its own comment; if Option A ever
lands, *that* is the moment to wire it against real semantics.

**(c) Session strategy:** One long-lived Crush session per h2 agent session,
but prefer explicit `--session <id>` over `--continue`. `--continue` resumes
"most recent in cwd" — implicit state that goes wrong under concurrency and is
untestable. Capture the id after the first turn (diff
`crush session list --json` before/after, or extract from verbose output —
verify which works on v0.91.0), emit EventSessionStarted so the daemon persists
it as HarnessSessionID (`monitor.go:490` path), map `SupportsResume()=true` to
`--session <ResumeSessionID>`. Fallback: with per-agent `--data-dir` (§2),
`--continue` becomes unambiguous per-agent and is acceptable if id capture
proves flaky.

**(d) Version pinning:** Yes. There is no existing codex version gate to copy
(grep found none) — add a small startup check in PrepareForLaunch/EnsureConfigDir:
exec `crush --version`, require the pinned floor (v0.91.0), fail with an
actionable error. Pre-1.0 tool + upstream docs already diverging from the pinned
CLI makes this mandatory, not optional. Also check for a
disable-autoupdate env analogue (opencode needed `OPENCODE_DISABLE_AUTOUPDATE=1`).

## 5. max_tokens gotcha — right catch, two refinements

1. Make large/small max_tokens role-configurable template vars instead of
   constants in EnsureConfigDir, with a comment explaining the OpenRouter 402
   worst-case-cost rejection — otherwise someone will "fix" 4096 back to 64000.
   Note 4096 can truncate long coding turns (big diffs); acceptable, but the
   role prompt should tell the agent to put artifacts in files, not chat.
2. Surface errors properly: on exit ≠ 0, pattern-match stderr (402/payment,
   auth, rate limit) and emit the monitor's existing
   `EventServerErrorInfo`/`EventAuthErrorInfo` (`monitor.go:384-397`) so the
   status bar shows the cause instead of silently going Idle. Machinery exists;
   the harness just needs the mapping.

## 6. Smaller notes

- **Interrupt:** spell out Ctrl+C handling: 0x03 hits the supervisor; it must
  kill/SIGINT the in-flight `crush run` and emit turn-failed → Idle, otherwise
  an interrupted turn strands Active until the 2-min backlog watchdog.
- **Turn timeout:** add a configurable supervisor-level timeout (kill + failed)
  so a hung crush process can't wedge an agent indefinitely.
- **Permissions list drift:** unit-test that the generated `crush.json` parses
  and contains the expected `allowed_tools` names — catches silent tool renames
  at version bumps (v0.91.0 already diverges from main-branch docs).
- **Metrics will read zero** (no OTEL from crush): EventsReceived=false in the
  status bar. Acceptable; state it so nobody debugs a non-bug. No
  NativeLogPathSuffix applies either (SQLite, not files) — say so explicitly to
  preempt a pointless codex-style tailer.
- **Tests:** §6 is solid. Add: two-agents-same-cwd isolation test (§2); shim
  stub tests (exit 0 / exit 1 / SIGINT / timeout); hook-envelope test against
  `handle_hook.go`'s required `hook_event_name`; EnsureConfigDir idempotency
  with `setupFakeHome(t)` per AGENTS.md. Note e2e tests live in `tests/external`
  (Makefile skips them in CI) — the opencode doc's `e2etests/` path doesn't exist.
- **Rollout:** additive package + blank import beside
  `session.go:31-34` + one role yaml; PR-first per repo convention; rollback =
  revert blank import. Matches the opencode doc's posture. Good.
- **Doc nits:** fix the codex/generic characterization in §2 (see §1); label the
  §1 memory numbers as harness-only (each h2 daemon+VT adds overhead); Related
  links should include `internal/session/message/delivery.go` since reply/input
  plumbing is central to Q(b).

## Bottom line

Approve the direction (exec turn-done, XDG isolation, pinned config, version
pin). Required before implementation: resolve child-process ownership (§1,
recommend supervisor shim), same-cwd data-dir isolation (§2), and the
system-prompt delivery mechanism (§3). Everything else is refinement.
