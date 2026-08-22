# Code Review: telegram-routing tg1.1+tg1.2 (R1, grok-reviewer)

- Bead: tg1.1 + tg1.2 (no bd id on branch)
- Commit range: `d21ea1f` → `03e2a36` (tg1.1 HTML default + mirror) → `230a138` (tg1.2 reply-context)
- Plan doc: `docs/plans/telegram-routing.md`
- Reviewer: grok-reviewer
- Scope: 8 files, +692/−25. Did not edit the `telegram-routing` worktree. `06d50fe` (tg1.3 script conversion changelog) is after this range and was not reviewed. `TestPoll_ExponentialBackoff` is the known tg1.5 flake — not gated.

Verified (this worktree, flake skipped):

```
go test ./internal/bridge/telegram/ ./internal/bridgeservice/ ./internal/config/ ./internal/bridge/tghtml/ \
  -count=1 -timeout 120s -skip TestPoll_ExponentialBackoff
```

all `ok`. `gofmt -l` clean on the touched Go files.

## Review focus (concierge)

| Check | Result |
|---|---|
| Mirror is best-effort / non-blocking — Send never fails or waits on the concierge sink | **Pass** |
| Untagged outbound HTML-escaped for `<`, `&`, `>` so Telegram cannot 400 | **Pass** |
| Reply-context: prefix-strip + ~1500-rune truncation | **Pass** |
| Tests use `setupFakeHome` / `CheckTestIsolation` where they touch config; no real config dir | **Pass** (see P3) |

## Findings

### P2 - HTML passthrough (`LooksLikeHTML`) does not escape raw `&`

**Location:** `internal/bridge/tghtml/convert.go` `downconvert`; `html.go` `HTML`

**Problem**
Untagged prose goes through `Render` → `escapeText`, which rewrites `<` `&` `>` (covered by `TestSend_EscapesUntaggedSpecials` and `TestRender` `"escape text runs"`). That is the plan's requirement.

If the body already contains a Telegram tag (`<b>`, `<code>`, …), `LooksLikeHTML` is true and `downconvert` copies non-tag bytes verbatim. A mixed body like `<b>ok</b> a & b` keeps a raw `&`, which `parse_mode=HTML` will 400. Send then falls back to plain `sendMessage` (no parse_mode), so the user still gets the text, but **formatting is dropped**.

Also: `<(b|strong|…)\s` means a prose fragment `see <b foo` can be classified as HTML.

Not in the untagged path the concierge asked about; still a 400→fallback hole for already-tagged senders.

**Suggested fix**
In `downconvert`, entity-escape `&` `<` `>` in text runs the same way `escapeText` does (leave existing `&amp;` / `&lt;` / `&gt;` alone). Tighten `LooksLikeHTML` to require a closing `>` on the opening tag.

---

### P2 - Mirror `deliverRequest` has no deadline; a stuck concierge leaks goroutines

**Location:** `internal/bridgeservice/service.go` `mirrorOutbound` / `deliverRequest`; `internal/bridge/telegram/rich.go` `mirror`

**Problem**
The user-facing send is correctly fire-and-forget: `mirror` `Add`s a WaitGroup, starts a goroutine with panic recover, and returns. `TestSend_SlowMirrorDoesNotBlockSend` (200ms) and `TestSend_ServiceMirrorDownDoesNotFailSend` (no socket) cover the two cases that matter for Send.

If the concierge socket exists but the process is wedged, `net.Dial` + `SendRequest` + `ReadResponse` can block that goroutine indefinitely. Each outbound then leaves one parked goroutine. `Telegram.Close` waits on `mirrorWG`, so bridge shutdown would hang too.

**Suggested fix**
`context.WithTimeout` around `deliverRequest` (a couple of seconds is plenty for a local unix send). Log and return on deadline. Optionally cap in-flight mirrors.

---

### P3 - `telegram_test.go` / `service_test.go` do not call `CheckTestIsolation`

**Location:** `internal/bridge/telegram/telegram_test.go`, `internal/bridgeservice/service_test.go`

**Problem**
Plan §4: use `setupFakeHome(t)` + `config.CheckTestIsolation()`. The tests that actually load YAML (`TestLoadFrom_MirrorTarget`) do both.

Telegram and bridgeservice tests never call `config.ConfigDir()` — they use `httptest` and `t.TempDir()` sockets — so they cannot hit the real config dir. Letter-of-plan gap only.

**Suggested fix**
A one-liner `t.Cleanup` / package `TestMain` that calls `CheckTestIsolation` in those packages, or leave as-is since they do not resolve H2_DIR.

---

### P3 - Reply-quote truncation is runes but untested with non-ASCII; only one `[…]` prefix is stripped

**Location:** `telegram.go` `quoteReplyOriginal`; `bridge.StripH2Envelope`

**Problem**
Implementation uses `[]rune` and `maxReplyQuoteRunes = 1500`, then appends `…`. Table test only uses ASCII `x`. `StripH2Envelope` strips a single leading `[…]` (agent tag **or** `[h2 message from: …]`). A stacked header would leave the second bracket group in the quote. Current outbound to the chat is a single `[agent] ` tag, so this matches the plan.

**Suggested fix**
One table row with a 1501-rune string containing non-ASCII (e.g. `"ä"` / `"🙂"`). Optional: loop-strip while the remainder still matches `^\[[^\]]+\]\s*`.

---

## What landed (mapped to the plan)

### 3a. HTML default + outbound mirror

- `Send` / stream `Close` always try `tghtml.HTML` then `sendMessage` with `parse_mode=HTML`. Confirmed by `TestSend` (`gotMode == "HTML"`).
- Untagged `<` `&` `>` become `&lt;` `&amp;` `&gt;` before that call (`TestSend_EscapesUntaggedSpecials`).
- HTML or persist failure falls back to **plain** `sendMessage` (no parse_mode) of the original text, then still mirrors if the fallback succeeded. A failed send does **not** mirror (`TestSend_FailedSendDoesNotMirror`).
- `Telegram.Mirror` is invoked only after a successful user send, in its own goroutine, errors logged, panics recovered. `streamMu` is not held across the sink.
- Config: `TelegramConfig.MirrorTarget *string` — omitted = live concierge, `""` = off, `"name"` = fixed. Wired from `bridge_daemon.go` into `ServiceOpts`. Tests: `TestLoadFrom_MirrorTarget` (with `setupFakeHome` + `CheckTestIsolation`), `TestConfigureMirror_*`, `TestMirrorOutbound_*`, `TestSend_MirrorsThroughServiceToConcierge`.

### 3b. Reply-context

- On `ReplyToMessage`, body becomes:

```
[in reply to]
> <stripped original, ≤1500 runes + …>
<user text>
```

- `StripH2Envelope` removes a leading `[h2 message from: …]` / `[agent]` / `[h2 trigger …]` header. Empty / envelope-only originals leave the user body unchanged.
- Routing still uses `ParseAgentTag` on the replied-to text when the new message has no `agent:` prefix (`TestStartStop_ReplyRouting`).
- `TestWithReplyContext` covers tag strip, h2 envelope, multiline `> ` quoting, empty, envelope-only, untagged original, and 1500-rune truncation.

### 3c. Script conversion

Out of this range (tg1.3 / `06d50fe`). Not reviewed.

## Tests vs plan §4

| Plan test | Present |
|---|---|
| default `parse_mode=HTML` | `TestSend` |
| HTML-escape untagged specials | `TestSend_EscapesUntaggedSpecials` |
| mirror enqueued on send | `TestSend_MirrorsToSink`, stream `TestStreamClose_MirrorsToSink`, service `TestSend_MirrorsThroughServiceToConcierge` |
| mirror failure does not fail send | `TestSend_MirrorFailureDoesNotFailSend`, `TestSend_ServiceMirrorDownDoesNotFailSend` |
| slow sink does not delay Send | `TestSend_SlowMirrorDoesNotBlockSend` |
| reply-context in handler | `TestStartStop_ReplyRouting` |
| no-reply body unchanged | existing inbound tests + empty-original row |
| `setupFakeHome` + `CheckTestIsolation` | config YAML tests; not needed in httptest packages |

## Summary

2 findings: **0 P0, 0 P1, 2 P2, 2 P3**.

**Verdict**: **Approved**.

The four review-focus items hold in the code and in tests I ran (excluding the tg1.5 backoff flake). Mirror cannot fail or delay `Send` even with no concierge socket; untagged `<` `&` `>` are escaped under `parse_mode=HTML`; reply-context strips the leading `[…]` envelope and truncates at 1500 runes. P2s (tagged `&` passthrough, unbounded mirror dial) are worth a follow-up bead, not a merge block.

Do not treat `TestPoll_ExponentialBackoff` as a regression.

## Disposition

Proposed to grok-reviewer 2026-08-22 (P2/P3, implicit consent). Implemented in the incorporation commit.

| # | Severity | Finding | Disposition | Commit | Notes |
|---|----------|---------|-------------|--------|-------|
| 1 | P2 | HTML passthrough (`LooksLikeHTML`) does not escape raw `&` | Incorporated | eb9c13b | `downconvert` now runs `escapeText` on non-tag text runs; `LooksLikeHTML` requires a closing `>`. Tests: `TestHTML_DownconvertsRich` mixed `&`/`<`, `TestLooksLikeHTML` `see <b foo`, `TestSend_EscapesAmpersandInsideTaggedHTML`. |
| 2 | P2 | Mirror `deliverRequest` has no deadline | Incorporated | eb9c13b | Mirror path only: `deliverRequestWithTimeout` with 2s dial+deadline (`mirrorDeliverTimeout`). Inbound `sendToAgent` stays unbounded. `TestMirrorOutbound_StuckPeerTimesOut`. |
| 3 | P3 | `telegram_test.go` / `service_test.go` do not call `CheckTestIsolation` | Not Incorporated | — | They never resolve H2_DIR (httptest + TempDir sockets). Reviewer already offered "leave as-is". |
| 4 | P3 | Truncation untested with non-ASCII; only one `[…]` prefix stripped | Incorporated | eb9c13b | Table rows for 1501×`ä` and 1501×`🙂`; `quoteReplyOriginal` loop-strips stacked envelopes. |
