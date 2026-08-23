# Telegram TUI Mini App — Test Plan

Companion to `telegram-tui-miniapp.md`. The design doc covers unit/component/
integration tests that live with the code. This doc covers the **additional
harnesses** that give correctness confidence beyond those: blackbox e2e, load,
security, test doubles for external deps, and the human manual-QA gate.

Same gating as the design doc — these are built alongside the feature once the
Ox-scheduler + cutover gate opens. Not before.

---

## 1. Blackbox e2e (core flows)

**Location:** `e2etests/tuistream/`. **Runner:** `make test-tuistream`
(new target; also folded into `make test-external`). **CI:** PR check (fast
subset) + nightly (full).

Flows, each a Go test driving the real gateway + a real throwaway h2 agent:
- **F1 list.** Start 2–3 real agents, hit `GET /api/agents` with a valid signed
  initData, assert names/roles/harness/state match `h2 list` output.
- **F2 live stream fidelity.** Agent runs a fixture TUI that paints a known
  ANSI screen (a deterministic pattern + a moving cursor). Attach via WS, feed
  received bytes into a headless VT emulator (`hinshun/vt10x`), assert the cell
  grid equals the golden screen. This is the "it actually renders" gate.
- **F3 resize.** Send a resize over WS, assert the fixture reflows and the
  emulated grid matches the new dimensions.
- **F4 interactive.** With an `input`-scoped session, type a command into the
  fixture (which echoes), assert echo appears; with a `view` session, assert the
  same keystrokes are dropped server-side.
- **F5 agent death.** Kill the agent mid-stream, assert the WS closes with the
  typed "agent ended" reason and the client sees it.

## 2. Security harness

**Location:** `e2etests/tuistream/security_test.go`. **Runner:**
`make test-tuistream`. **CI:** PR check (these are cheap and critical).

- **S1** forged/absent initData → 401 on HTTP and rejected WS upgrade.
- **S2** valid initData but non-owner `user.id` → 403, logged, no socket dial.
- **S3** expired `auth_date` beyond the window → 401 (replay protection).
- **S4** WS upgrade without a valid session token → rejected; token for agent A
  can't open agent B; expired token rejected; spent token can't be reused.
- **S5** view-scope token attempting PTY write → dropped + connection flagged.
- **S6** gateway binds 127.0.0.1 only — assert it is not reachable on a public
  interface in the test env.
- **S7** CSP / `frame-ancestors` / WS `Origin` checks reject non-Telegram
  origins.

## 3. Load / stress

**Location:** `e2etests/tuistream/load_test.go` + `make bench-tuistream`.
**CI:** on-demand + nightly (not PR).

- **L1 fan-out.** N concurrent WS clients (up to `max_sessions` and one over,
  asserting the cap rejects the overflow) each attached to a chatty agent;
  measure per-frame added latency and memory; assert no goroutine/fd leak
  (compare `runtime.NumGoroutine` and open fds before/after).
- **L2 slow client / backpressure.** A deliberately slow-reading WS client
  against a high-output agent; assert the coalescing ring keeps memory bounded
  and the client converges to the latest screen (feeds the §12/E2 measurement
  in the design doc). Golden metric: < 250 ms added latency at 3 Mbps, bounded
  RSS.
- **L3 soak.** 30-min attach with periodic reconnects; assert stable memory and
  no descriptor growth. Nightly only.

## 4. Test doubles for external deps

- **D1 Fake agent socket** (`e2etests/tuistream/fakeagent`): a Go helper that
  speaks the real `attach` protocol (`message.WriteFrame` of scripted
  `FrameTypeData`/`FrameTypeControl`), so gateway tests need no real Claude/crush
  process. Reused by component + e2e tests.
- **D2 Telegram initData signer** (`e2etests/tuistream/tgsign`): produces valid
  and deliberately-invalid `initData` blobs given a test bot token, so auth
  tests don't depend on Telegram. Mirrors Telegram's documented HMAC scheme.
- **D3 No live Telegram / cloudflared in automated tests.** Those are exercised
  only in manual QA (§5). The gateway is tested directly over loopback.

## 5. Manual QA (human-judgement gate, every release)

Runs on a real phone through real Telegram + cloudflared. Checklist owner: user.
- **M1** Menu button / `/agents` opens the Mini App; agent list is correct and
  live (start/stop an agent, pull-to-refresh reflects it).
- **M2** Open an agent mid-work: colours, box-drawing, cursor, and a real
  full-screen TUI (e.g. the crush/claude UI, or `htop`) render correctly;
  scrollback scrolls; latency feels live.
- **M3** Rotate the phone / fullscreen toggle: reflow is correct, safe-area
  insets respected.
- **M4** Arm interactive, run a harmless command, confirm it lands; disarm,
  confirm input is ignored.
- **M5 Negative (must fail closed):** open `public_url` from a second Telegram
  account and from a plain desktop browser → blocked (Cloudflare Access +
  initData). Confirm nothing renders and the attempt is logged.

## 6. Coverage & measurement

- `make test` reports coverage for `internal/tuistream`; target ≥ 85% on
  `auth.go` and `wsproxy.go` (the risk-bearing files).
- `make bench-tuistream` emits latency/throughput/RSS numbers; the PR must paste
  them. Regressions > 20% block merge.
- Security tests (§2) are mandatory PR checks; a red S-test blocks merge with no
  override.
