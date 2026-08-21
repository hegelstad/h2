# Code Review: opencode harness oc1.1 (R1, grok-reviewer)

- Bead: oc1.1 (no bd id on branch)
- Commit: `086b687` (`feat(harness): add hook-driven opencode package (oc1.1)`)
- Plan doc: `docs/plans/opencode-harness.md`
- Reviewer: grok-reviewer
- Review commit: (this file)
- Scope: the 8 files under `internal/session/agent/harness/opencode/` at 086b687. Did not edit `coder-oc-h2`. Later commits on `feat/opencode-harness` (`8a57d95` oc1.2 auth, `4d8d9a7` oc1.3 session/handle-hook/roles wiring) are noted only when they already close a finding.

Verified: `go test ./internal/session/agent/harness/opencode/` at 086b687 passes; `gofmt -l` clean; `go vet` clean. Probed installed opencode **1.18.21** (`opencode debug paths` / `debug config`) with the harness env.

## Findings

### P1 - Isolation is not airtight: `OPENCODE_CONFIG_DIR` does not redirect Path.config; `XDG_CONFIG_HOME` / `XDG_STATE_HOME` / `XDG_CACHE_HOME` are unset

**Location:** `internal/session/agent/harness/opencode/harness.go` (`BuildCommandEnvVars`)

**Problem**
The package correctly sets both env vars the plan called out:

- `OPENCODE_CONFIG_DIR` → `HarnessConfigDir()`
- `XDG_DATA_HOME` → `<cfg>/data`

Probed against opencode 1.18.21:

| env | `debug paths` result |
|---|---|
| none | `data=~/.local/share/opencode`, `config=~/.config/opencode` |
| **only** `OPENCODE_CONFIG_DIR` | **data still `~/.local/share/opencode`** (auth.json leak — this is why XDG_DATA_HOME is required) |
| `OPENCODE_CONFIG_DIR` + `XDG_DATA_HOME` 110590d | `data=<cfg>/data/opencode` (auth.json + sqlite + logs isolated) but **`config` stays `~/.config/opencode`**, cache `~/.cache/opencode`, state `~/.local/state/opencode` |
| those + `XDG_CONFIG_HOME`/`STATE`/`CACHE` | all four redirect |

`opencode debug config --print-logs` with the harness env still does:

```
loading path=/home/ubuntu/.config/opencode/config.json
loading path=/home/ubuntu/.config/opencode/opencode.json
loading path=/home/ubuntu/.config/opencode/opencode.jsonc
loading config from OPENCODE_CONFIG_DIR path=<isolated>
```

So creds (auth.json under `XDG_DATA_HOME/opencode/`) are isolated — the key finding is half-done — but the managed child still **merges the host user's global config and will load `~/.config/opencode/plugins/`**. Two opencode agents will not collide on auth.json; they *will* inherit whatever the ubuntu user has globally, and a background `npm install` was observed against `dir=/home/ubuntu/.config/opencode` when a plugin was present.

`OPENCODE_CONFIG_DIR` *does* load isolated `opencode.json` and auto-discovers `<cfg>/plugins/h2-bridge.ts` (`plugin_origins` listed it). Plugin format (named export + `event` hook) matches the 1.18.21 loader.

**Suggested fix**
In `BuildCommandEnvVars`, when `cfg != ""`, also set:

```go
env["XDG_CONFIG_HOME"] = filepath.Join(cfg, "xdg-config") // or cfg itself
env["XDG_STATE_HOME"]  = filepath.Join(cfg, "xdg-state")
env["XDG_CACHE_HOME"]  = filepath.Join(cfg, "xdg-cache")
```

Create those dirs in `EnsureConfigDir`. Add a test that all four XDG_* plus `OPENCODE_CONFIG_DIR` are set and none equal the real `~/.config/opencode` / `~/.local/share/opencode` (capture the real home *before* `t.Setenv("HOME", ...)` — see P2 below).

---

### P1 - `session.idle` is droppable under backpressure (`pushState` is fully non-blocking)

**Location:** `internal/session/agent/harness/opencode/events.go` (`pushState`); plugin also fires on every `message.updated`

**Problem**
Claude already hit this class of bug (`h2-wkg`): a dropped Idle leaves `IsIdle` false and withholds queued `h2 send`. Claude's handler uses `emitTerminal` (block up to 2s) for Stop/SessionStart/SessionEnd. This harness does:

```go
select {
case h.stateCh <- stateChange{state: state, sub: sub}:
default: // idle is gone
}
```

`stateCh` is buffered 32. The plugin maps `message.updated` → `opencode.session.active` (a per-message event, not per-token, but still several per turn). Child PTY starts *before* `harness.Start` (`session.go` StartPTY then `startAgentPipeline`), so hooks can enqueue before the drain loop is running.

A fill-then-idle sequence drops Idle; `Start` then seeds Active and drains 32 Actives as no-ops. The 2-minute idle-staleness watchdog (`maybeReconcileIdle`) is a backstop *only when there is a message backlog* — so this is a 2-minute stall, not a 9h wedge, but it is the same failure mode the plan was written to avoid.

Unknown / empty names return `ok=false` and do not panic (good). Malformed JSON payload is ignored (`_ = payload`) (good). Missing idle has no in-harness fallback (plan URP §7 ptycollector secondary is not in this commit — acceptable as a later bead, but then idle must not be lossy).

**Suggested fix**
Do not drop Idle/error:

- Mirror Claude `emitTerminal` for `StateIdle` (and maybe `session.error`).
- Or coalesce: keep a latest-state cell instead of a 32-deep queue so a trailing idle always wins.
- Plugin: do not hook `message.updated` (high frequency). `session.status` / `session.created` / `permission.asked` are enough to go Active; `session.idle` is the turn-done signal. Every `message.updated` is also a full `h2 handle-hook` subprocess.

Add `TestHandleHookEvent_IdleNotDroppedWhenBufferFull` (fill 32 actives, send idle, Start, assert Idle).

---

### P1 - Tests cover the happy isolation + idle paths, not the isolation hole or idle-loss paths

**Location:** `harness_test.go`, `events_test.go`

**Problem**
What is covered, and it is real:

- `TestBuildCommandEnvVars_IsolatesConfigAndData` requires both `OPENCODE_CONFIG_DIR` and `XDG_DATA_HOME`, under the prefix, with managed-agent flags.
- `TestOpencode_EmitsOnlyOnTransition` seeds Active, idle → Idle, duplicate idle suppressed, active → Active.
- `TestHandleInterrupt_ForcesIdle`.
- `TestEnsureConfigDir_*` writes plugin+json, idempotent, does not clobber user edits.
- Compile-time `var _ harness.Harness = (*OpencodeHarness)(nil)` — interface contract matches Claude.

What is **not** covered, despite being the review focus:

- Isolation fails closed when prefix is empty (`EnsureConfigDir` no-op, **neither** isolation env set — fleet would leak to `~/.local/share/opencode`). oc1.3 now sets `opencode-config` in `agent_setup.go`; this package still silently skips.
- `XDG_CONFIG_HOME` / `STATE` / `CACHE` unset (P1 above).
- Idle dropped when `stateCh` is full.
- Unknown / empty / garbage event names besides the single `UserPromptSubmit` check.
- `opencode.session.error` and `opencode.permission.asked` mappings (and they are currently unreachable from the plugin — see P2).
- Checksum *upgrade* path (owned file + matching checksum + new template → rewrite). Only the "leave user edits" side is tested.
- `AGENTS.md` write from `SystemPrompt`/`Instructions`.
- `assertIsolated` calls `os.UserHomeDir()` *after* `t.Setenv("HOME", temp)`, so it never looks at the real `~/.config/opencode`. Vacuous leak check.

`TestNoScreenReader` only asserts the harness does not have `ReadScreen()` methods that nobody implements. Harmless, not load-bearing.

**Suggested fix**
Add the tests listed under the P1s above. Capture `os.UserHomeDir()` before mutating `HOME`. Assert `XDG_DATA_HOME` is not `filepath.Join(realHome, ".local/share")`. Table-test `MapHookEvent` for empty, unknown, error, permission.

---

### P2 - Plugin event names do not match `MapHookEvent` for permission / message.updated

**Location:** `assets/h2-bridge.ts` vs `events.go`

**Problem**
Plugin sends:

| opencode event | handle-hook name |
|---|---|
| `session.idle` | `opencode.session.idle` |
| `message.updated` | `opencode.session.active` |
| `permission.asked` | `opencode.session.active` |
| `session.error` | `opencode.session.error` |

`MapHookEvent` also accepts `opencode.message.updated` and `opencode.permission.asked` (→ `SubStatePermissionReview`), which the plugin **never emits**. Permission-asked therefore never surfaces as permission-review; it is just Active/Thinking. Idle/error names match.

Plugin also has no guard on `event` being missing (`event.type` will throw). Host *may* catch per-event; if it disables the plugin, idle dies. `.nothrow()` on the `h2` spawn swallows handle-hook failures with no log.

**Suggested fix**
Send `opencode.permission.asked` for `permission.asked`. Drop `message.updated` (see P1). Wrap the handler in try/catch. If `H2_AGENT_NAME` is empty, skip the spawn (session already injects `H2_ACTOR`, but `--agent ""` is sloppy).

---

### P2 - Checksum sidecar is not atomic with the content write; missing upgrade test

**Location:** `config.go` `writeGuarded`

**Problem**
Content is write-temp + fsync + rename (good). Checksum is a follow-up `os.WriteFile(path + ".h2-opencode-checksum")`. Crash between rename and checksum: next `EnsureConfigDir` sees a file with empty stored sum and **treats it as user-owned**, so template/plugin updates never apply.

User-edit protection works (tested). Owned-file template upgrade is implemented but untested.

**Suggested fix**
Write checksum first to a temp + rename, or write both into a small manifest and replace together. Test: write owned content+checksum, change `pluginSource`/template, `EnsureConfigDir`, assert rewrite.

---

### P2 - Profile-scoped dirs share `AGENTS.md` and sqlite across agents

**Location:** `config.go` `EnsureConfigDir` / `dataDir`; `HarnessConfigDir()` = prefix + profile

**Problem**
Same pattern as Claude (`claude-config/<profile>`), so sharing *credentials* across agents on one profile is intended. Opencode is different in two ways:

1. System prompt is written to `<cfg>/AGENTS.md` (Claude passes `--system-prompt` per process). Two opencode roles on `profile: default` last-writer-win the prompt.
2. 1.18.21 stores sessions in `<XDG_DATA_HOME>/opencode/opencode.db` (WAL). Two concurrent children share one db.

**Suggested fix**
Namespace data (at least) by `AgentName`: `XDG_DATA_HOME = filepath.Join(cfg, "data", rc.AgentName)`. Keep `OPENCODE_CONFIG_DIR` profile-scoped for the plugin + `opencode.json` if you want shared auth, or write `AGENTS.md` per-agent if prompts differ.

---

### P3 - `opencode.json.tmpl` special-cases Ox Alpha; model string is unescaped

**Location:** `assets/opencode.json.tmpl`, `renderConfigJSON`

**Problem**
Plan §6: "Ox Alpha is not special-cased anywhere in Go." The Go is model-agnostic; the template hard-codes `provider.openrouter.models["stealth/ox-alpha"]` limits. Other models get provider defaults. `text/template` interpolates `{{.Model}}` with no JSON escape — a model id containing `"` would break the file. Roles are trusted, so this is a nit.

**Suggested fix**
Keep the generous output limit as a generic `provider.openrouter.options` (if the schema allows) or document it as an Ox-Alpha overlay. Use `json.Marshal` for the model string.

---

### P3 - Plugin `h2` binary resolution and PATH

**Location:** `assets/h2-bridge.ts`

**Problem**
`$`h2 handle-hook ...`` depends on `h2` being on the child's PATH. Claude settings.json does the same, so this matches the fleet. Worth knowing if a managed agent sanitizes PATH.

**Suggested fix**
Optional: inject `H2_BIN` from `BuildCommandEnvVars` (`os.Executable()` or `lookPath`) and call that.

---

## Out of scope for 086b687 (already on `feat/opencode-harness`)

These were plan §3/§11 items **not** in the 8-file commit. They are **not** defects of oc1.1, and oc1.3 already landed them:

- Blank import `_ "…/harness/opencode"` in `session.go` + `resolve_test.go`.
- `handle_hook.go --event` (plugin CLI would have failed cobra unknown-flag + required stdin `hook_event_name` on 086b687 alone; oc1.3 adds `--event` and allows empty stdin).
- `agent_setup.go` prefix for `opencode` → `<H2Dir>/opencode-config`.
- `h2 auth opencode` (oc1.2).

oc1.4 signoff still needs the P1s in this package; wiring later beads does not close them (`pushState` and `BuildCommandEnvVars` are unchanged after oc1.3 except the stashed key file).

## Plan / CLAUDE harness contract

| Plan / interface item | 086b687 |
|---|---|
| `Harness` methods | yes (`var _ harness.Harness`) |
| `Name`/`Command`/`DisplayCommand` = `opencode`; `SupportsResume` | yes |
| Resume: `-s <ResumeSessionID>` only | yes (mirrors Claude `--resume` only) |
| Fresh: `-s` + `-m` | yes |
| `--agent` opencode-agent mapping | no (plan was conditional; P3) |
| System prompt via config dir / `AGENTS.md` | yes, untested |
| `OPENCODE_PERMISSION=bypass`, disable autoupdate/share | yes |
| `H2_AGENT_NAME` | yes |
| Hook-driven Start, no screen scrape, transition-only emit | yes |
| `HandleOutput` no-op, `HandleInterrupt` force-idle | yes |
| Checksum-guarded config + plugin embed | yes |
| `session.idle` → Idle | yes (lossy under full buffer) |
| Plugin-failure ptycollector fallback (URP) | not in this bead |
| `h2 auth opencode` / role yaml / blank import | later beads |

## Empirical notes (opencode 1.18.21)

- `auth.json` path in the bundle is `join(XDG_DATA_HOME, "opencode", "auth.json")` else `~/.local/share/opencode/auth.json`. **Not** under `OPENCODE_CONFIG_DIR`. Setting only `OPENCODE_CONFIG_DIR` leaks creds. This commit does set `XDG_DATA_HOME`.
- Local plugins auto-load from `OPENCODE_CONFIG_DIR/plugins/*.ts` (named or default export). Confirmed via `debug config --pure` listing `file://…/plugins/h2-bridge.ts`.
- Presence of a plugin triggers a background npm install in both `~/.config/opencode` and `OPENCODE_CONFIG_DIR` (observed `NpmInstallFailedError` when killed). Harmless if install is truly background for the TUI; still a reason to isolate `XDG_CONFIG_HOME`.

## Summary

5 findings to act on: **0 P0, 3 P1, 3 P2, 2 P3** (P2/P3 counts: 3 P2, 2 P3).

**Verdict**: **Approved with revisions**. Not clean for oc1.4 signoff until the P1s land:

1. Set `XDG_CONFIG_HOME` (and state/cache) so isolation is actually airtight — `OPENCODE_CONFIG_DIR` + `XDG_DATA_HOME` is necessary and **not sufficient**.
2. Make Idle non-lossy (Claude `emitTerminal` / coalesced latest-state); stop spawning handle-hook on every `message.updated`.
3. Tests for (1) and (2), plus empty/malformed hook names. Happy-path isolation + transition tests are good and already green.

Unit tests at 086b687 are green (`go test ./internal/session/agent/harness/opencode/`). `make check` was not run on the full tree in this worktree (package `gofmt`/`vet` clean).

## Disposition

Proposed to grok-reviewer 2026-08-22 (h2). P1s match the review's suggested fixes (oc1.4 gate); implementing without waiting on a reject. P2 #6 deferred with follow-up bead.

| # | Severity | Finding | Disposition | Commit | Notes |
|---|----------|---------|-------------|--------|-------|
| 1 | P1 | Isolation: XDG_CONFIG/STATE/CACHE unset; Path.config still ~/.config/opencode | Incorporated | 110590d | IsolationEnv sets OPENCODE_CONFIG_DIR + all four XDG_* under cfg. EnsureConfigDir mkdirs them and fails closed if cfg is empty. Same env used by `h2 auth opencode`. Tests capture real home in TestMain before mutating HOME. |
| 2 | P1 | session.idle droppable (`pushState` non-blocking 32-deep queue) | Incorporated | 110590d | Coalesce mailbox (latest-state cell + 1-slot signal). Plugin no longer hooks message.updated; session.created/status + permission.asked → Active. TestHandleHookEvent_IdleNotDroppedWhenBufferFull. |
| 3 | P1 | Tests miss isolation hole and idle-loss paths | Incorporated | 110590d | Empty-prefix fail-closed, XDG quartet + host-home leak check, idle-not-dropped, MapHookEvent table (empty/unknown/error/permission), checksum upgrade, AGENTS.md write. |
| 4 | P2 | Plugin event names vs MapHookEvent for permission / message.updated | Incorporated | 110590d | Emits `opencode.permission.asked`; drops message.updated; try/catch; skip spawn if H2_AGENT_NAME empty. |
| 5 | P2 | Checksum sidecar not atomic with content; missing upgrade test | Incorporated | 110590d | Missing checksum treated as incomplete write (rewrite). Checksum via temp+rename. User-edit protection remains checksum-present + content mismatch. Tests: upgrade owned file; rewrite when checksum missing. |
| 6 | P2 | Profile-scoped AGENTS.md and sqlite shared across agents | Deferred | — | Namespacing XDG_DATA_HOME by AgentName would split auth.json away from `h2 auth opencode`'s profile-level data dir. Shared profile creds match Claude. Follow-up bead workspace-oc1.5. oc1.4 is one agent on profile=default. |
| 7 | P3 | opencode.json.tmpl special-cases Ox Alpha; model string unescaped | Incorporated | 110590d | encoding/json for the whole file. Generous context/output limits applied to the configured model's OpenRouter key (strip `openrouter/` prefix), not a hardcoded stealth/ox-alpha-only block. |
| 8 | P3 | Plugin `h2` binary resolution / PATH | Incorporated | 110590d | BuildCommandEnvVars sets H2_BIN from os.Executable() (LookPath fallback); plugin uses `process.env.H2_BIN \|\| "h2"`. |
| 9 | P1 | Interactive `opencodeAuthEnv` appends XDG_* (first-wins leak) | Incorporated | c494140 | Combined oc1.1+oc1.2 review. ApplyIsolationEnv strips parent OPENCODE_CONFIG_DIR + XDG_* then sets isolated values. Tests: exactly one XDG_DATA_HOME, leaky parent gone; bare `h2 auth opencode` invokes login with isolated env even if OPENROUTER_API_KEY is set. Flag-present (`--openrouter-key=`) selects stash/lookup. |
