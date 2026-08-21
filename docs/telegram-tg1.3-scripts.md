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
