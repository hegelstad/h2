# Design: Bot API rich drafts as the only outbound Telegram path

## Summary

Outbound Telegram no longer has a `--format` flag. Every send goes through
Bot API 10.1 rich messages. A draft is **not** a message: `sendRichMessageDraft`
returns `True` and expires in 30 seconds. Persistence is always
`sendRichMessage`. `editMessageText` revises an already-persisted message; it
cannot finalize a draft.

Plain `sendMessage` is retained only as a failure fallback, with the original
unmodified text. Losing a user-visible message because rich failed is the
failure mode this design exists to prevent.

Inbound photo/document handling stays gone (accepted in the revert).

## Architecture

```mermaid
flowchart LR
  CLI["h2 send"] --> Sock["unix socket"]
  Sock --> Svc["bridgeservice"]
  Svc --> TG["telegram.Telegram"]
  TG --> R["richhtml.Render"]
  TG --> Draft["sendRichMessageDraft"]
  TG --> Persist["sendRichMessage"]
  TG --> Edit["editMessageText"]
  TG --> Plain["sendMessage fallback"]
  Draft -.->|any error| Plain
  Persist -.->|any error| Plain
  Edit -.->|any error| Plain
```

```mermaid
sequenceDiagram
  participant CLI
  participant TG as Telegram
  participant API as Bot API

  Note over CLI,API: One-shot (args or --file)
  CLI->>TG: Send(complete text)
  TG->>TG: Render(html)
  TG->>API: sendRichMessage(html)
  API-->>TG: Message
  Note over TG: no draft

  Note over CLI,API: Stream (--stdin)
  CLI->>TG: StreamOpen
  loop chunks + 30s refresh
    CLI->>TG: StreamWrite
    TG->>API: sendRichMessageDraft(draft_id, html)
    API-->>TG: True
  end
  CLI->>TG: StreamClose
  TG->>API: sendRichMessage(html)
  API-->>TG: Message

  Note over CLI,API: Any rich step fails
  TG->>API: sendMessage(original text)
```

```mermaid
stateDiagram-v2
  [*] --> OneShot: complete body
  [*] --> Streaming: --stdin
  OneShot --> Persisted: sendRichMessage ok
  OneShot --> Fallback: sendRichMessage fail
  Streaming --> Streaming: draft update / refresh
  Streaming --> Persisted: sendRichMessage on close
  Streaming --> Fallback: any draft/persist fail
  Fallback --> [*]: sendMessage original text
  Persisted --> Revised: editMessageText (later)
  Persisted --> [*]
```

## Facts pinned from the Bot API docs

These are not negotiable; they contradict an earlier sketch that treated a
draft as an editable message.

1. `sendRichMessageDraft` streams an ephemeral 30-second preview and returns
   `True`. You **must** then call `sendRichMessage` to persist.
2. Draft params: `chat_id` (Integer, **private chat only**), optional
   `message_thread_id`, `draft_id` (Integer, **required, non-zero**; same id
   animates), `rich_message` (`InputRichMessage`, no direct file upload).
3. `editMessageText` accepts `rich_message` and a real `message_id`. Correct
   for revising a persisted message. Wrong for finalizing a draft.
4. `InputRichMessage`: exactly one of `html`, `markdown`, `blocks`. We emit
   `html` only.
5. Raw newlines collapse to whitespace. Structure is tags. `<div>` is not
   supported. Named entities allowed: `&lt; &gt; &amp; &quot; &apos; &nbsp;
   &hellip; &mdash; &ndash; &lsquo; &rsquo; &ldquo; &rdquo;`. Everything else
   must be numeric.
6. Limits: 32768 UTF-8 chars, 500 blocks (incl. nested / list items / table
   rows / details), 16 nest levels, 50 media attachments, 20 table columns.
7. `<tg-thinking>` is valid **only** in drafts, never in the persisted body.

## User-facing interface

No `--format`. Two ways to supply a body, same as today, plus one streaming
source:

```
h2 send telegram "hello"          # one-shot: persist only
h2 send telegram --file body.txt  # one-shot: persist only
h2 send telegram --stdin          # stream drafts, then persist
```

`--stdin` errors if stdin is a TTY (do not hang the CLI). Args/`--file` never
fake a draft by slicing an already-complete string.

## Internal API

### Renderer — `internal/bridge/richhtml`

Pure, no I/O. Heavy table-driven tests live next to it.

```go
package richhtml

type Options struct {
    Thinking bool // wrap a trailing <tg-thinking>…</tg-thinking>; drafts only
}

func Render(text string, opt Options) (html string, err error)
```

Deterministic mapping of **agent-written text with real newlines** (not full
Markdown):

| Input | Output |
| --- | --- |
| Blank-line-separated blocks | `<p>…</p>` |
| Single newline inside a block | `<br>` |
| Lines starting with `- ` or `* ` | `<ul><li>…</li></ul>` |
| Lines matching `^\d+[.)] ` | `<ol><li>…</li></ol>` |
| Fenced ` ```lang ` … ` ``` ` | `<pre><code class="language-lang">…</code></pre>` |

Non-goals (leave as paragraph text): ATX headings, thematic breaks, inline
`*bold*` / `` `code` ``, tables, blockquotes. Do not grow this into a Markdown
parser unless a later design says so.

Text-run escaping:

1. Emit only tags from the documented rich-html set (never `<div>`).
2. Escape raw `&`, `<`, `>` in text runs **after** structure is decided, so
   renderer-owned tags stay intact.
3. Legal named entities pass through. Any other `&name;` is rewritten to
   `&#N;` via a bundled HTML5 named-character map; unknown names become
   `&amp;name;`.

`Render` returns an error if the result exceeds 32768 characters or 500
top-level-ish blocks (we count `<p>`, `<li>`, `<pre>`, headings we do not
emit). Caller treats that as a rich failure and falls back.

### Orchestrator — `internal/bridge/telegram`

`Sender.Send` is the one-shot path (what `bridgeservice` already calls):

```
render → sendRichMessage(html) → on any error: sendMessage(original)
```

Streaming is a writer on the same type, driven by the CLI over the socket
(see below). It is **not** implemented by chunking a complete string.

```go
type Stream struct { /* draft_id, buf, lastHTML, lastFlush */ }

func (t *Telegram) OpenStream(ctx context.Context) *Stream
func (s *Stream) Write(p []byte) (int, error) // append, maybe draft
func (s *Stream) Close() error                // persist; fallback on error
```

`draft_id`: `atomic.Uint64` on `Telegram`, starting at 1, skip 0 on wrap.
Stable for the life of one `Stream`. Distinct for the next `OpenStream`.
Not a second-resolution timestamp.

Draft flush policy:

- Flush on newline or every 200ms of new bytes, whichever first.
- If the stream stays open with no new bytes, refresh the **same**
  `draft_id` with the last HTML at 20s so the 30s preview does not expire.
- Draft HTML may include `<tg-thinking>…</tg-thinking>` (truncated tail or
  a fixed "generating" marker). The persist call never includes it.

On the first draft API error (including "not a private chat"), abandon the
draft loop and fall back to `sendMessage` of the original accumulated text.
Same if `sendRichMessage` fails after drafts were shown. Log the reason at
`log.Printf` with the API description. Fallback uses the existing
`sendChunk` / `SplitMessage` 4096 path. If fallback also fails, return that
error — that is the only way a message is lost.

`editMessageText` is an internal method (`EditRich(ctx, messageID, text)`)
used to revise a persisted message. v1 CLI does not expose it; `Send` /
`Stream.Close` store the last persisted `message_id` on `Telegram` so a later
caller can revise. Do not invent a `--format` or `--edit` flag in this
change.

### Socket protocol — `internal/session/message` + `internal/bridgeservice`

One-shot stays `Request{Type:"send", Body, From, …}`.

Streaming adds three types so the bridge process (not the CLI) owns the
Bot API loop:

| Type | Meaning |
| --- | --- |
| `send_stream_open` | allocate `draft_id`, return `stream_id` |
| `send_stream_write` | append `Body` to that stream (may draft) |
| `send_stream_close` | persist |

The CLI `--stdin` path is the only producer of these types. `bridgeservice`
routes them to `Telegram.OpenStream` / `Write` / `Close`. Agent-tag prefix
is applied to the first write (same rule as `sendOutbound` today).

```
cmd/send  --(send)-->  bridgeservice  --Send-->  telegram
cmd/send  --(stream)-->  bridgeservice  --Open/Write/Close-->  telegram
```

`cmd` does not import `telegram`. `telegram` does not import `cmd`.
`richhtml` is imported only by `telegram` (and its tests).

## Fallback contract (most important behaviour)

Trigger fallback when **any** of these happen:

- HTTP/API error from draft, persist, or edit
- Draft refused because the chat is not private
- `Render` error (over limit, internal)
- Malformed markup rejected by the API

Fallback payload is the **original unmodified text**, never the rendered
HTML. Log why. Tests must cover each failure point independently.

## Testing

All of these are unit/component tests under `make test`. None touch the real
config dir (`setupFakeHome` / `config.CheckTestIsolation` from PR #9).

| What | Where | Runner |
| --- | --- | --- |
| Renderer table (paragraphs, `<br>`, ul/ol, fences, escaping, entities, limits, no `<div>`) | `internal/bridge/richhtml/render_test.go` | `make test` |
| Property: every escaped text run round-trips through a tiny unescape of the legal set | `internal/bridge/richhtml/render_test.go` | `make test` |
| httptest Bot API: one-shot calls **only** `sendRichMessage` with `rich_message.html` | `internal/bridge/telegram/rich_test.go` | `make test` |
| httptest: stream is `N × sendRichMessageDraft` (same non-zero `draft_id`) then one `sendRichMessage` | `internal/bridge/telegram/rich_test.go` | `make test` |
| httptest: each failure point (draft 4xx, persist 4xx, not-private, over-limit) issues `sendMessage` with original text | `internal/bridge/telegram/rich_test.go` | `make test` |
| One-shot never emits a draft | `internal/bridge/telegram/rich_test.go` | `make test` |
| `draft_id == 0` never sent | `internal/bridge/telegram/rich_test.go` | `make test` |
| Persist HTML never contains `<tg-thinking>` | `internal/bridge/telegram/rich_test.go` | `make test` |
| `--stdin` on a TTY errors; `--stdin` from a pipe drives stream types | `internal/cmd/send_test.go` | `make test` |
| `sendOutbound` still one-shots `Sender.Send` (no format field) | `internal/bridgeservice/service_test.go` | `make test` |

`make check` (gofmt, vet, staticcheck) stays required.

## Unreasonably robust programming

- Fallback is total: every rich error path is a named test, not a shared
  helper that could skip a case.
- Draft refresh at 20s is tested with a fake clock, not a 20s sleep.
- Renderer refuses to emit unsupported tags (`<div>` included) via a
  denylist assertion on every table case's output.
- `draft_id` generator is tested across wrap (skip 0).

## Out of scope

- Inbound media (explicitly dropped).
- `--format` in any form.
- Full Markdown / CommonMark.
- Fake streaming by chunking a complete `--file` body.
- Revising a persisted message from the CLI (internal method only in v1).
