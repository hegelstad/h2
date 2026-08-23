# Changelog

## Unreleased

### New Features

- **Rate-limit bridge notifications**: When an agent hits a Claude/Codex usage
  limit, h2 now sends a one-time alert over every running bridge (e.g.
  Telegram) with the agent name, profile, reset time, and a `h2 rotate` hint.
  The notification is best-effort, fires once per distinct limit (deduped via
  the profile's `ratelimit.json`), runs off the monitor goroutine, and reaches
  the user even though the limited agent itself cannot make model calls (the
  bridge is a separate process). Costs no model tokens.

### Changes

- **Revert custom Telegram formatting layer**: Removed `--format`
  (HTML / MarkdownV2 / rich / rich-html), the `FormattedSender` and
  `RichSender` interfaces, and inbound photo/document handling. This is
  a deliberate revert of the custom formatting layer, not a regression:
  the Bot API rich-draft path will replace it. Outbound Telegram is
  again a plain `sendMessage` of the unmodified text. Losing inbound
  image/file handling is accepted.
- **Telegram chat HTML (not rich documents)**: `h2 send` to Telegram
  persists via `sendMessage` + `parse_mode=HTML` so replies look like
  normal chat bubbles. While the target agent is active, the bridge
  refreshes the classic blue typing indicator (`sendChatAction`)
  every 4s. There is no Thinking preview and no `sendMessageDraft`.
  `--stdin` buffers and sends one `sendMessage` on close. Rich-only
  tags (`<p>`, `<ul>`, …) are downconverted. `sendRichMessage` is
  not used. No `--format` flag. If HTML is rejected, the original
  text is sent plain.

### Bug Fixes

- **Attach first-try drop**: `ReadResponse`/`ReadRequest` no longer use
  `json.Decoder`, which buffered past the handshake newline and could
  swallow the first binary attach frames. First `h2 attach` after a cold
  start then desynced and exited with no error. Handshake JSON is now
  read one byte at a time through the terminating newline.

- **Test isolation guard**: `setupFakeHome` now points `H2_DIR` at a
  temp h2 directory with a marker and fails the test if `ResolveDir`
  still lands on the host config dir. Previously the helper set
  `H2_DIR=""` and resolution walked up into the real tree, so tests
  could reach a live Telegram socket.
### Bug Fixes

- **grok: reliably receive and reply to h2 messages**: The Grok Build harness now
  derives idle/active state from rendered TUI screen content instead of output
  silence. Grok Build's TUI repaints continuously (animated spinner + elapsed
  clock), so it never goes output-silent — the previous ptycollector-based
  detector could therefore never report idle, and normal-priority inter-agent
  messages (including `--expects-response` replies) were never delivered. State
  is now classified from the TUI's own indicators, scoped by REGION so marker
  text appearing in the conversation transcript (a user quoting a marker, grok's
  reply containing `[stop]`, grok viewing `classify.go` itself) can never pin the
  agent Active: `Waiting for response` / `[stop]` are honored only on the status
  line above the input box and only when an animated braille spinner co-occurs
  there, and `Esc:cancel` only in the footer below the box. Idle is debounced and
  an unrecognized screen holds last state and logs. The submit path (`text` +
  50ms + `\r`) is unchanged, and a Ctrl+C-forced idle is briefly protected from a
  still-painted turn marker undoing it. Markers are centralized in
  `harness/grok/classify.go` and pinned against real captured screen frames
  (idle-at-prompt, composing, active, and multi-line-active).

### Internal

- New `harness.ScreenReader` seam and `VT.ScreenText()` accessor let a harness
  read the live rendered screen; wired by the session for screen-state harnesses.
- New on-demand live smoke test (`e2etests`, build tag `grok_live`) drives real
  grok through the VT + classifier + submit path end to end, plus a frame-capture
  tool (build tag `grok_capture`) to regenerate the classifier fixtures.
- **PTY idle detector no longer floods the event stream**: `ptycollector` now
  emits state updates only on genuine idle<->active transitions. Previously it
  emitted an `active` update on every output signal, so a chatty child (e.g. a
  streaming CLI harness like Grok Build) produced thousands of duplicate
  `state_change` events, ballooning the per-agent event log until the harness
  was OOM-killed and restarted into the same loop. `SignalInterrupt` is now
  routed through the run loop so state tracking stays consistent.

## v0.3.2

### New Features

- **Ctrl+Space mode switching**: Added ctrl+space as an alternative to ctrl+\
  for switching between modes (normal, menu, passthrough). Helps users with
  keyboard layouts that lack a backslash key (e.g. Norwegian).

## v0.3.1

### New Features

- **Session restarted/rotated events**: New `session_restarted` and `session_rotated`
  event types for triggers, with `H2_OLD_PROFILE` and `H2_NEW_PROFILE` env vars
  on rotate events.
- **Next fire time in schedule list**: `h2 schedule list` now shows a NEXT column
  with the computed next fire time.
- **Auto-ID for triggers/schedules**: Empty IDs are auto-generated, so role YAML
  only needs `name`.
- **Default role automations**: Opinionated default role now includes auto-rotate-on-limit
  trigger, resume-after-restart trigger, and check-assigned-beads schedule.
- **Expanded concierge role template**: Comprehensive coordination instructions and
  agent-nudge schedule.
- **Reduced minimum tile pane width**: From 79 to 59 columns, allowing 3-wide layouts
  on standard laptop screens.

### Bug Fixes

- **Rotate fails to move session log**: `NativeLogPathSuffix` was set in memory by
  `PrepareForLaunch` but never persisted to disk for Claude Code sessions, causing
  `moveSessionLog` to skip the file during rotation.
- **Session log tailer replays old rate limits after rotate**: The moved session log
  was read from the beginning, replaying old events. Now seeks to end on resume.
- **Expired ratelimit.json not cleaned up**: Stale files accumulated on disk and
  poisoned rotate profile selection. Now cleaned up on read when expired.
- **Bridge prefix parsing fails on multiline messages**: The agent prefix regex used
  `.*` which doesn't match newlines, causing multiline messages to fall through to
  concierge routing.
- **Automation commands run in wrong CWD**: Trigger/schedule conditions and exec
  actions now run in the agent's configured working directory.
- **Triggers/schedules not reloaded on restart**: `h2 session restart` now reloads
  automations from the updated RuntimeConfig.
- **Schedule start time alignment**: Schedules without explicit start times now
  truncate to the top of the minute for clean firing times.
- **Harness/profile order in h2 list**: Now shows `claude [default]` instead of
  `[default] claude`.
- **Generic bridge naming in pod template**: Replaced hardcoded username with
  `user-{codename}` convention.

## v0.3.0

Major release adding an automation system (triggers, schedules, expects-response),
DCG permission hooks, tiled terminal layouts, pod templates, and session management
improvements. **Contains breaking changes** — see details below.

### Breaking Changes

- **`--responds-to` renamed to `--closes`**: The flag for message-triggered
  responses has been renamed. Update any scripts or role configs using the old name.
- **`h2 rotate` moved to `h2 session rotate`**: The rotate command is now a
  subcommand of `session`.
- **`make test-all` removed**: `make test` now runs all tests (without `-short`).
  Use `make test` for the full suite.
- **`e2etests/` moved to `tests/external/`**: The e2e test directory has been
  relocated. The make target is now `test-external`.
- **Pod simplification**: Pod paths flattened, pod-scoped roles removed, overrides
  and bridges added. Existing pod configurations will need to be updated.

### New Features

#### Automation System (Triggers & Schedules)

A full automation system for event-driven and time-based agent actions:

- **Triggers**: Fire actions on events (message received, state change, etc.)
  with condition evaluation and environment variable support.
- **Schedules**: RRULE-based recurring actions with cron-style scheduling.
- **Expects-response**: Syntactic sugar over triggers for tracking message
  reply expectations. Use `--closes` flag on `h2 send`.
- **Repeating triggers**: Triggers that fire repeatedly on matching events.
- **CLI commands**: `h2 trigger` and `h2 schedule` subcommands with full
  CRUD and socket protocol support.
- Compensating trigger removal on message send failure.
- Heartbeat migrated to the new schedule system.

#### DCG Permission Hooks

- Integrated dcg-go as a native Go library (no more shelling out to binary).
- `permission_decision` events emitted to `events.jsonl` for all tool use.
- DCG hook handler uses `EvaluateToolUse` for all tools.
- Added `very-strict` policy support.

#### Tiled Ghostty Layouts

- `h2 attach --tile` for automatic tiled split layouts in Ghostty.
- `--dry-run` support for tile layout preview.
- Switched from `ghostty +action` CLI to Ghostty AppleScript API.
- Auto-detect full window size for overflow tabs.
- Fill columns before rows in tile layout; equalize splits after grid build.

#### Pod Templates

- `h2 pod create` and `h2 pod update` commands.
- Embedded `dev-pod` template with codename and profile variable support.
- `.yaml.tmpl` extension support for pod templates.
- Arithmetic template functions and proper `.Index`/`.Count` rendering.

#### Session Management

- **Session rotate**: Auto-select next profile, support globs and candidate
  lists, auto stop/resume running agents.
- **Codex resume support**.
- **Rate limit tracking**: `ratelimit.json` written on usage limit events,
  limits shown in profile commands, skipped during rotate.

#### UI Improvements

- Input bar stash mode, priority preservation, and Enter routing fixes.
- Steer backlog display with skip-idle-first.
- Age filters (`--older-than`, `--newer-than`) on `h2 list`.
- `--include-stopped` flag and session cleanup with `--older-than`.
- Harness types shown in profile list and show commands.
- Pod YAML agent ordering preserved in list and tile attach.
- Auto-attach pod launch in Ghostty.

#### Planning & Review Skills

- `plan-to-beads`: Decompose plan docs into implementation tasks with
  dependencies.
- `plan-seam-review`: Review interfaces between connected plan components.
- `code-review` and `code-review-incorporate`: Structured code review process.
- `plan-work-completion-signoff`: Verify implementations against plans.
- `e2e-wiring-review`: End-to-end wiring audit for any project type.
- Shared skill scripts directory in profile template system.
- Implementation Guide concept added to planning skills.

### Bug Fixes

- Fixed daemon crash resilience: stderr logging, panic recovery, nil guards,
  data race fixes.
- Converted all VT mutex manual unlocks to `defer` with panic recovery.
- Fixed PipeOutput crash on terminal resize (Content/Format mismatch).
- Fixed RenderInputBar panic on narrow terminal (tab switch crash root cause).
- Fixed attach panic recovery and narrow resize edge cases.
- Fixed Codex agent stuck in `active/tool_use` after tool completion.
- Fixed agent state stuck on `Exited` after relaunch.
- Fixed bridge relaunch race: wait for socket cleanup before re-fork.
- Fixed pod external tests: removed pod-scoped roles, aligned with current
  pod system.
- Fixed Codex SSE rate limit detection.
- Fixed condition environment: merge base env into condition evaluation.
- Fixed expects-response trigger ID mismatch on collision retry.
- Sped up automation tests with mock Clock/Timer interfaces.
- Removed slog from automation package, fixed schedule test races.

### Build and CI

- Added e2e tests to CI workflow.
- Makefile reorganization: `test-external` target, auto-detach support.
- `make check` required before feature commits.

### Documentation

- Public configuration docs added.
- Design docs for triggers/schedules, expects-response, pod simplification,
  and RuntimeConfig.
- Concrete test location and make target requirements in all plan docs.

## v0.2.0

Major release that refactors agent architecture, simplifies role configuration,
and adds agent naming features. **Contains breaking changes** — see the
migration guide below.

### Breaking Changes

#### Session metadata format change (RuntimeConfig)

`session.metadata.json` has been replaced with a unified `RuntimeConfig` format
that stores the fully-resolved session configuration. This affects `h2 run --resume`:

- **Resume of sessions started before this change will attempt a legacy fallback.**
  If the old metadata can be parsed, it is automatically converted to RuntimeConfig
  format. If the old metadata is missing required fields (e.g. `harness_type`),
  the resume will fail with a clear error naming the missing fields.
- **The daemon CLI (`_daemon`) now accepts only `--session-dir`** instead of ~15
  individual flags. This is an internal interface — no user action needed.
- Session metadata now includes all resolved config fields (instructions, model,
  permissions, heartbeat, additional dirs) so resumed sessions use the exact same
  configuration as the original launch.

#### Role config field renames and removals

The role YAML schema has changed significantly. Existing role files will need
to be updated.

**Renamed fields:**

| v0.1.0 | v0.2.0 | Notes |
|---|---|---|
| `name` | `role_name` | Frees `name` for future use; clarifies intent |
| `agent_type` | `agent_harness` | Values: `claude_code` (default), `codex`, `generic` |
| `model` | `agent_model` | Empty means use agent app's own default |

**Removed fields:**

| v0.1.0 field | Replacement |
|---|---|
| `permissions.allow` | Use harness-native configs (e.g. Claude Code `allowedTools` in settings) |
| `permissions.deny` | Use harness-native configs |
| `permissions.agent` | Moved to top-level `permission_review_agent` |

**New fields:**

| Field | Description |
|---|---|
| `agent_name` | Template-rendered agent name (see Agent Naming below) |
| `agent_harness_command` | Command override for any harness |
| `profile` | Account profile name (default: `default`) |
| `codex_sandbox_mode` | Codex `--sandbox` flag (`read-only`, `workspace-write`, `danger-full-access`) |
| `codex_ask_for_approval` | Codex `--ask-for-approval` flag (`untrusted`, `on-request`, `never`) |
| `permission_review_agent` | AI permission reviewer (replaces `permissions.agent`) |
| `claude_code_config_path` | Explicit Claude Code config path override |
| `claude_code_config_path_prefix` | Prefix for Claude Code config paths |
| `codex_config_path` | Explicit Codex config path override |
| `codex_config_path_prefix` | Prefix for Codex config paths |

#### Default model and permission flags no longer forced

h2 no longer injects default `--model` or `--permission-mode` flags when
launching agents. If these fields are empty in the role config, the agent
harness uses its own defaults. This makes agent behavior more predictable and
avoids overriding user-level configurations.

#### Permission model simplified

The unified `approval_policy` field (which mapped to each harness's native
permission flags) has been removed in favor of using harness-native fields
directly:

- **Claude Code**: Set `claude_permission_mode` (maps to `--permission-mode`)
- **Codex**: Set `codex_ask_for_approval` (maps to `--ask-for-approval`) and
  `codex_sandbox_mode` (maps to `--sandbox`)

This gives you direct control over each harness's permission system without
an abstraction layer in between.

### Migration Guide

Update your role YAML files from v0.1.0 to v0.2.0 format:

```yaml
# v0.1.0
name: my-role
agent_type: claude
model: sonnet
claude_permission_mode: plan
permissions:
  agent:
    enabled: true
    instructions: "Review all file writes"
instructions: |
  You are a helpful assistant.

# v0.2.0
role_name: my-role
agent_harness: claude_code
agent_model: sonnet
claude_permission_mode: plan
permission_review_agent:
  enabled: true
  instructions: "Review all file writes"
instructions: |
  You are a helpful assistant.
```

For Codex roles:

```yaml
# v0.1.0
name: codex-role
agent_type: codex
model: gpt-5.3-codex

# v0.2.0
role_name: codex-role
agent_harness: codex
agent_model: gpt-5.3-codex
codex_ask_for_approval: on-request
codex_sandbox_mode: workspace-write
```

### Agent Naming

Roles can now specify an `agent_name` field with template functions for
automatic name generation:

- **`{{ randomName }}`** — generates a random `adjective-noun` name with
  collision detection against running agents
- **`{{ autoIncrement "prefix" }}`** — scans running agents for
  `<prefix>-1`, `<prefix>-2`, etc. and returns the next number

Examples:

```yaml
role_name: worker
agent_name: "{{ randomName }}"
instructions: |
  Your name is {{ .AgentName }}.
```

```yaml
role_name: builder
agent_name: '{{ autoIncrement "builder" }}'
instructions: |
  You are {{ .AgentName }}.  # resolves to builder-1, builder-2, etc.
```

The agent name is resolved via two-pass template rendering — first pass
resolves the `agent_name` field, second pass re-renders the full YAML with
the resolved name available as `{{ .AgentName }}`.

The CLI `--name` flag still takes precedence over the role's `agent_name`
field.

### Architecture: Agent Harness Refactor

The internal agent architecture has been significantly refactored. The
previous `AgentType` + `AgentAdapter` pattern has been replaced with a
unified `Harness` interface:

- **`harness.Harness`** interface — `BuildCommandArgs()`, `BuildCommandEnvVars()`,
  `PrepareForLaunch()`, `HandleEvent()`, `EnsureConfigDir()`
- **`harness/claude/`** — Claude Code harness implementation
- **`harness/codex/`** — Codex harness implementation
- **`harness/generic/`** — Generic command harness

The old `adapter/`, `agent_type.go`, and `AgentWrapper` have been removed.
The `Agent` struct now owns a single `Harness` directly.

Other internal refactors:
- Legacy OTEL layer removed; metrics naming unified
- `Note*` signaling methods renamed to `Signal*`
- Event flow and shared collectors unified across harnesses
- Claude hooks, OTEL, and session logs unified in a single event handler
- `OutputCollector` extracted to `shared/outputcollector/`
- `EventStore` extracted to `shared/eventstore/`
- `OTELServer` extracted to `shared/otelserver/`
- Agent monitor (`AgentMonitor`) extracted for event consumption, state
  tracking, and metrics

### Terminal and UI Improvements

- **Scroll mode**: Added PageUp/PageDown/Home/End support for navigating
  scroll history. PageUp and Home now enter scroll mode from normal mode.
- **Scroll mode exit**: Auto-exit scroll mode only triggers at bottom for
  mouse wheel and End key (not for other navigation).
- **Screen flashing fix**: Replaced erase-line with erase-to-end-of-line to
  prevent full-screen flashes during redraws.
- **Scroll regions**: Fixed scrollback for apps using scroll regions (e.g.
  Codex). `PlainHistory` removed; `ScrollHistory` used when app uses scroll
  regions.
- **Alternate scroll mode**: Added support for CSI?1007h (mouse-to-arrow
  conversion in alternate screen).
- **CSI passthrough**: Unhandled CSI sequences (PageUp, PageDown, Home, End)
  are now passed through to the child process.
- **White background fix**: Fixed background color bleed on hint text in
  live view.

### Codex Support

- Default Codex model updated to `gpt-5.3-codex`.
- Fixed Codex OTEL ingestion, event/state mapping, and debug tooling.
- Fixed OTEL trace exporter format for Codex.
- Codex config directory support with per-profile paths.
- Removed `--no-alt-screen` from Codex args (no longer needed).

### Bug Fixes

- Fixed message delivery to use resolved `H2_DIR` instead of hardcoded `~/.h2`.
- Fixed activity log and OTEL logs to use resolved `H2_DIR`.
- Prevented duplicate bridge processes with socket probe on startup.
- Fixed terminal color propagation for Codex rendering.
- Cache `TERM` and `COLORTERM` in `terminal-colors.json` for background
  launches.
- Fixed `dry-run` formatting: full args display, proper JSON encoding of
  instructions, copy-pasteable command output, correct column alignment.

### Build and Tooling

- Added Makefile with `build`, `test`, `check` (vet + staticcheck), and
  `test-coverage` targets.
- Fixed `vet` and `staticcheck` findings.
- Added CI workflow and `check-nofix`, `test-e2e`, and `loc` targets.

### Profile and Role Management

- Added role inheritance with deep-merge semantics and preserved YAML node
  merge/tag handling.
- Added profile commands (`create`, `list`, `show`, `update`) and unified
  profile/role creation paths across `init` and role commands.
- Added style-based `init` templates and generated harness config support.
- Clarified and expanded role/template docs for inheritance and variable
  contracts.

### Bridge and Runtime Improvements

- Refactored `h2 bridge` into subcommands and added bridge-service concierge
  lifecycle management.
- Added bridge service lifecycle integration tests and fixed race conditions.
- Improved terminal render behavior with synchronized output handling and
  VT capability query responses.
- Added stricter run preflight checks for socket/daemon state and harness
  configuration.

### Planning Skills and Docs

- Added planning lifecycle skills: `plan-architect`, `plan-draft`,
  `plan-review`, `plan-incorporate`, and `plan-summarize`.
- Added `plan-orchestrate` skill for coordinating plan generation/review
  end-to-end.
- Improved skill docs and parser logic for disposition table handling and
  multi-round review reporting.

## v0.1.0

Initial release.
