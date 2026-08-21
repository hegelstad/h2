# Code Review: opencode harness oc1.1+oc1.2 (R1, grok-reviewer)

- Bead: oc1.1 + oc1.2 (no bd id on branch)
- Commit range: `086b687`..`8a57d95` (inclusive: oc1.1 package + oc1.2 `h2 auth opencode`)
- Plan doc: `docs/plans/opencode-harness.md`
- Reviewer: grok-reviewer
- Scope: reviewed as a unit per concierge. Did not edit `coder-oc-h2`. oc1.3 (`4d8d9a7` session/handle-hook/roles) is noted only when it already closes a finding.

Verified: `go test ./internal/session/agent/harness/opencode/` and `go test ./internal/cmd/ -run 'Opencode|OpenRouter|AuthOpencode'` pass on this worktree. Probed installed opencode **1.18.21** (`opencode debug paths` / `debug config`).

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
| `OPENCODE_CONFIG_DIR` + `XDG_DATA_HOME` (this commit) | `data=<cfg>/data/opencode` (auth.json + sqlite + logs isolated) but **`config` stays `~/.config/opencode`**, cache `~/.cache/opencode`, state `~/.local/state/opencode` |
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

### P1 - Interactive `opencode auth login` env appends without replacing existing `XDG_DATA_HOME` / `OPENCODE_CONFIG_DIR`

**Location:** `internal/cmd/auth_opencode.go` `opencodeAuthEnv`

**Problem**
The interactive path *does* intend to set both isolation vars (the concierge's checklist item):

```go
env := append(os.Environ(),
    "OPENCODE_CONFIG_DIR="+cfgDir,
    "XDG_DATA_HOME="+filepath.Join(cfgDir, "data"),
)
```

PTY launch (`StartPTY`) **filters then overrides** existing keys. This path does not. Duplicate keys in `cmd.Env` are first-wins for libc `getenv` (glibc, and typically Bun). If the parent already has `XDG_DATA_HOME` (systemd user session, a wrapper, a leftover export), `opencode auth login` writes `auth.json` to the **parent** data dir (`~/.local/share/opencode/auth.json`), not `<cfg>/data/opencode/auth.json`.

`TestOpencodeAuthEnv_PointsInsideConfigDir` walks the slice and keeps the **last** match, so it would pass in the duplicate-key case while the child uses the first. There is no test that `runOpencodeLogin` is invoked with a filtered env when no key is stashed.

The non-interactive stash itself is correct (see "what oc1.2 got right" below).

**Suggested fix**
Reuse the `filteredEnv` pattern from `StartPTY` / `internal/session/daemon.go`: drop existing `OPENCODE_CONFIG_DIR`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_CACHE_HOME`, then set the isolated values (including config/state/cache — same P1 as the harness). Test: `t.Setenv("XDG_DATA_HOME", "/tmp/leaky")`, build env, assert **exactly one** `XDG_DATA_HOME=` and that it is `<cfg>/data`. Add `TestAuthOpencode_NoKey_InvokesLoginWithIsolatedEnv` that stubs `runOpencodeLogin` and inspects the env.

---

### P2 - `h2 auth opencode` with no flag still stashes and skips login if env/secrets have a key

**Location:** `internal/cmd/auth_opencode.go` `runAuthOpencode`

**Problem**
Plan: flag selects the mode — bare `h2 auth opencode` → interactive `opencode auth login`; `--openrouter-key` → stash (and may read env/secrets). Implementation always `resolveOpenRouterKey(flag, secrets)` and if anything is found, stashes and **returns without login**. A host `OPENROUTER_API_KEY` or `~/h2home/.secrets.env` makes interactive zen/provider login unreachable. Documented in Long help, but it collapses the two modes.

**Suggested fix**
Only resolve env/secrets when `--openrouter-key` was actually passed (use `cmd.Flags().Changed("openrouter-key")`, allowing empty flag to mean "look up"). Bare `h2 auth opencode` always runs isolated login.

---

### P3 - Secrets path is hardcoded `~/h2home/.secrets.env`, not `H2_DIR`

**Location:** `defaultSecretsPath`

**Problem**
Plan literally says `~/h2home/.secrets.env`, so this matches the machine. `config.ConfigDir()` / `H2_DIR` would survive a relocated data dir. `parseEnvFile` also does not strip quotes or `export ` prefixes.

**Suggested fix**
`filepath.Join(config.ConfigDir(), ".secrets.env")` with the documented path as fallback. Optional quote-strip.

---

## What oc1.2 got right (isolation checklist)

| Check | Result |
|---|---|
| `--openrouter-key` stash path | `<config-dir>/openrouter.key` — the same dir the harness uses as `OPENCODE_CONFIG_DIR` (`HarnessConfigDir()` = prefix/`default`) |
| stash mode 0600 | yes; `TestStashOpenRouterKey_IsolatedAndMode600` asserts `Perm()==0o600` |
| stash not in `~/.config/opencode` | writes only under the provided cfg dir (tests use `t.TempDir()`) |
| default dir | `<H2Dir>/opencode-config/default` — profile-scoped, same convention as `h2 auth claude` (not per-agent-name; shared key for the profile is intended) |
| interactive sets `OPENCODE_CONFIG_DIR` **and** `XDG_DATA_HOME=<cfg>/data` | yes, the strings are right; merge is wrong (P1 above) |
| mkdir data 0700 before login | yes |
| non-interactive does not spawn login | yes (`TestAuthOpencode_OpenRouterKeyFlag_DoesNotInvokeLogin`) |
| harness reads stash if env unset | yes (`TestBuildCommandEnvVars_ReadsStashedKeyFile`); process env wins over stash |

---

## Out of scope for 086b687..8a57d95 (already on `feat/opencode-harness` as oc1.3)

These were plan §3/§11 items **not** in this range. oc1.3 already landed them:

- Blank import `_ "…/harness/opencode"` in `session.go` + `resolve_test.go`.
- `handle_hook.go --event` (plugin CLI would have failed cobra unknown-flag + required stdin `hook_event_name` on 086b687 alone; oc1.3 adds `--event` and allows empty stdin).
- `agent_setup.go` prefix for `opencode` → `<H2Dir>/opencode-config`.

oc1.4 signoff still needs the P1s; oc1.3 does not close them (`pushState` unchanged; `BuildCommandEnvVars` only gained the stash read; `opencodeAuthEnv` still appends).

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
| `h2 auth opencode` two modes | oc1.2: yes (stash 0600 + isolated login env, with P1 merge bug) |
| role yaml / blank import | oc1.3 |

## Empirical notes (opencode 1.18.21)

- `auth.json` path in the bundle is `join(XDG_DATA_HOME, "opencode", "auth.json")` else `~/.local/share/opencode/auth.json`. **Not** under `OPENCODE_CONFIG_DIR`. Setting only `OPENCODE_CONFIG_DIR` leaks creds. This commit does set `XDG_DATA_HOME`.
- Local plugins auto-load from `OPENCODE_CONFIG_DIR/plugins/*.ts` (named or default export). Confirmed via `debug config --pure` listing `file://…/plugins/h2-bridge.ts`.
- Presence of a plugin triggers a background npm install in both `~/.config/opencode` and `OPENCODE_CONFIG_DIR` (observed `NpmInstallFailedError` when killed). Harmless if install is truly background for the TUI; still a reason to isolate `XDG_CONFIG_HOME`.

## Summary

**0 P0, 4 P1, 4 P2, 3 P3.**

**Verdict**: **Approved with revisions**. Not clean for oc1.4 signoff until the P1s land:

1. Set `XDG_CONFIG_HOME` (and state/cache) on **both** the harness child and `opencodeAuthEnv` so isolation is airtight — `OPENCODE_CONFIG_DIR` + `XDG_DATA_HOME` is necessary and **not sufficient**.
2. `opencodeAuthEnv` must **replace** existing `XDG_*` / `OPENCODE_CONFIG_DIR` (filter, don't append). Today a parent `XDG_DATA_HOME` sends interactive `auth.json` to the host path. Tests currently last-wins and would miss it.
3. Make Idle non-lossy (Claude `emitTerminal` / coalesced latest-state); stop spawning handle-hook on every `message.updated`.
4. Tests for (1)–(3), plus empty/malformed hook names. Happy-path isolation + 0600 stash + transition tests are good and already green.

oc1.2 stash path + 0600 + "sets both env vars" intent are correct. The remaining auth isolation hole is the env merge, not the stash location.

Unit tests: `go test ./internal/session/agent/harness/opencode/` and `go test ./internal/cmd/ -run 'Opencode|OpenRouter|AuthOpencode'` green. `make check` was not run on the full tree.
