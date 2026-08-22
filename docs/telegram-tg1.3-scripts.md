# tg1.3 Phase A — script conversion changelog

Date: 2026-08-22
Agent: grok-tg
Branch: `feat/telegram-routing`
Live scripts live in `~/h2home/bin` (not git-tracked). This file is the
audit trail. Snapshots of the converted scripts are in
`docs/telegram-tg1.3-phaseA/`.

**Backup (do not delete; Phase B still needs it):**
`/home/ubuntu/h2home/bin.bak-tg1.3-20260822-005116`

**Not converted (Phase B / claude, or hybrid):**
`bridge-watchdog`, `concierge-restart-resume.sh`, `concierge-redeploy.sh`,
`reply-guard-healthcheck`, `health-nuisance-alert`, `pulse-meta-agent`,
`telegram-reply-guard`. Confirmed byte-identical to the backup after Phase A.

Verification is **static only** — no live Telegram sends.

Helper added: `telegram_env.py` now exports `h2_send_telegram`, `html_esc`,
`html_attr` (additive; `load_telegram_creds` unchanged for Phase B / sendDocument).

| script | old mechanism -> new | how static-verified |
|---|---|---|
| `btc-alert` | direct `api.telegram.org/sendMessage` + `parse_mode=HTML` -> `h2_send_telegram` (HTML already present) | `python3 -m py_compile`; `grep api.telegram.org` empty; `grep parse_mode` empty; `grep h2_send_telegram` hit |
| `eafieldnotes-weekly-pulse` | Bot API HTML post with plain fallback -> `h2_send_telegram(sanitize_html(msg))`; dynamic errors `html_esc`'d | `py_compile`; no `api.telegram.org`; no `parse_mode` assignment; sanitize_html kept for model output |
| `eafieldnotes-amazon-check` | Bot API HTML post with h2-plain fallback -> `h2_send_telegram`; book titles `html_esc`'d | `py_compile`; no `api.telegram.org`; no `parse_mode` |
| `jaktformidling-weekly-pulse` | Bot API HTML post with h2-plain fallback -> `h2_send_telegram`; fetch errors `html_esc`'d | `py_compile`; no `api.telegram.org`; no `parse_mode`; prompt still asks Claude for `<b>`/`<i>` |
| `slskd-watchdog` | `curl .../sendMessage` (no parse_mode) -> `$H2 send telegram --file`; HTML `<b>`/`<code>` | `bash -n`; `grep api.telegram.org` empty; alert bodies tagged |
| `pr-notify` | Bot API HTML post -> `h2_send_telegram`; title/body/branch/author/URLs escaped (`html_esc`/`html_attr`) | `py_compile`; no `api.telegram.org`; no `parse_mode` |
| `visual-verify` | `sendDocument` with caption field -> **keep** `sendDocument` (no caption); caption via `h2_send_telegram(html_escape(caption))` | `py_compile`; `api.telegram.org` remains **only** on `/sendDocument`; no `parse_mode`; caption path uses `h2_send_telegram` |
| `cax11-try-once` | `curl .../sendMessage` + `parse_mode=HTML` + `%0A` -> `$H2 send telegram --file` with real newlines; `html_escape` on IP/error | `bash -n`; `grep api.telegram.org` empty; `grep parse_mode` empty |
| `window-experiment-poll` | already `h2 send telegram`; `**bold**` / `` `code` `` -> `<b>` / `<code>`; values escaped | `bash -n`; remaining `**` none; uses `--file` |
| `mcp-auth-alert` | already `h2 send telegram --file`; `**bold**` -> `<b>` | `bash -n`; no `**` left in payload |
| `eafieldnotes-weekly-stats` | already `h2 send telegram`; `**bold**` / `` `code` `` -> `<b>` / `<code>`; jq `@html` on paths/queries; API error escaped | `bash -n`; `grep '\*\*'` empty |
| `context-fresh-notify` | already `h2 send telegram`; no markdown present -> wrapped `<b>Heads up:</b>` | `bash -n`; HTML tag present |
| `gh-activity-monitor` | already `h2 send telegram --file`; heading `**...**` -> `<b>`; jq emits `<a href>` with `@html`; dropped unused `telegram-env.sh` source | `bash -n`; jq filter compiles on `[]`; no `**` left |
| `coaching-checkin` | already instructs Claude to `h2 send telegram`; prompt now asks for HTML tags (`<b>`,`<i>`,`<code>`), not markdown | `bash -n`; both monday/friday prompts updated |
| `telegram_env.py` | creds loader only -> plus `h2_send_telegram` / `html_esc` / `html_attr` | `py_compile`; `html_esc('a < b & c > d')` -> `a &lt; b &amp; c &gt; d`; `load_telegram_creds` signature unchanged |

`h2 send telegram` is the choke point: the bridge (tg1.1) applies `parse_mode=HTML`
and mirrors `[telegram-out]` to concierge. Scripts no longer set `parse_mode`.

---

# tg1.3 Phase B — recovery/self-heal surface

Date: 2026-08-22
Agent: claude coder-1
Branch: `feat/telegram-routing`
Snapshots: `docs/telegram-tg1.3-phaseB/`.

These 7 are the self-heal / recovery surface. Treatment follows the
**bridge-dependency rule**: a script's failure/alert must never depend on the
very component it is recovering. Concierge approved the split below on
2026-08-22 (failure alerts stay direct Bot API; informational "component is up"
alerts route through the bridge). Verification is **static only** — no live sends.

| script | old mechanism -> new | kept direct POST? why | how static-verified |
|---|---|---|---|
| `bridge-watchdog` | direct `api.telegram.org/sendMessage` (both alerts) -> **unchanged** | **YES, both alerts.** The script only runs on a down-bridge event; the "restart FAILED" alert must fire during a live outage, and even the "restart OK" alert fires seconds after the bridge was down + just restarted, so routing it through that same still-settling bridge is the exact chicken-and-egg the rule forbids. No bridge-is-healthy path exists here. Added a comment block at `alert()` so no future agent converts it. | `bash -n`; body byte-identical to backup apart from the comment block |
| `concierge-restart-resume.sh` | direct POST (3 alerts) -> **split**: 2 pod-up (success/marker-stuck) alerts to `h2 send telegram --file` + HTML (`<b>`/`<code>`); 1 "concierge did not come up" alert kept direct | **YES, the "did not come up" failure alert.** May coincide with a compound bridge+concierge outage → must bypass the path it is recovering. Success alerts are only reached after the daemon is confirmed running (bridge is never stopped, only re-pointed), so they route through the bridge for HTML + concierge mirror. | `bash -n`; `grep api.telegram.org` = exactly 1 (the failure `alert()`); no `parse_mode` |
| `concierge-redeploy.sh` | single trailing direct POST (MSG chosen by if/else) -> **split**: success branch to `h2 send telegram --file` + HTML; failure branch kept direct | **YES, the verification-failed branch.** Possible compound outage → independence. Bridge is never stopped (only re-pointed), so on success both pod+bridge are confirmed up and the confirmation routes through the bridge. | `bash -n`; `grep api.telegram.org` = exactly 1 (failure branch); `send telegram --file` present in success branch; no `parse_mode` |
| `reply-guard-healthcheck` | direct `urllib` `sendMessage` -> `h2_send_telegram` (via `send_telegram()` wrapper; kept for test `send_fn` injection) | **NO.** It watches `telegram-reply-guard` (a Stop hook), a *different* component from the bridge `h2 send` routes through — no chicken-and-egg. Problem strings now `html_esc`'d; header wrapped in `<b>`. | `py_compile`; 19/19 unit tests pass (inject `send_fn`, unaffected); no `api.telegram.org`; no `parse_mode` |
| `health-nuisance-alert` | direct `urllib` `sendMessage` + `parse_mode=HTML` -> `h2_send_telegram` | **NO.** Pure health notifier, unrelated to the messaging surface. `build_message()` already emits HTML; `place`/`art` now `html_esc`'d. | `py_compile`; `--selftest` OK; no `api.telegram.org`; no `parse_mode` set |
| `pulse-meta-agent` | direct `urllib` `sendMessage` + `parse_mode=HTML` -> `h2_send_telegram` | **NO.** Monthly calibration report, unrelated to recovery. **Bug fixed:** dynamic values (bot UA samples, Claude free-text output, error strings, threshold dicts) were interpolated into a `parse_mode=HTML` body **unescaped** — a UA/summary containing `<`/`&`/`>` could break the markup or drop the message. All such values are now `html_esc`'d at interpolation. | `py_compile`; `--dry-run` renders; no `api.telegram.org`; no `parse_mode` set |
| `telegram-reply-guard` | direct `urllib` `sendMessage` (degradation `alert()`) -> **unchanged (comment only)** | **YES.** (1) Recursion: it scans agent Stop-hook transcripts for the literal `h2 send telegram`; routing its own alert that way could feed its own watcher. (2) Independence: the alert fires precisely when h2's reply path is already suspect. Added an explanatory comment at the POST site (`alert()`) so no future agent "fixes" it. The normal reply *forward* (in `main`) still uses `h2 send telegram` — that is the intended h2 path and was already so. | `py_compile`; body byte-identical to backup apart from the comment block |

Kept-direct Bot API POSTs after Phase B (intentional, each documented above):
`bridge-watchdog` (both alerts), `concierge-restart-resume.sh` (failure alert),
`concierge-redeploy.sh` (failure branch), `telegram-reply-guard` (degradation alert).
