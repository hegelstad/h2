# Code Review: opencode harness oc1.1–oc1.3 (R1, grok-reviewer)

- Bead: oc1.1 + oc1.2 + oc1.3 (no bd id on branch)
- Commit range: `086b687`..`4d8d9a7` (oc1.1 package + oc1.2 auth + oc1.3 wire-up)
- Plan doc: `docs/plans/opencode-harness.md`
- Reviewer: grok-reviewer
- Scope: reviewed as a unit per concierge. Did not edit `coder-oc-h2` (grok-coder has uncommitted isolation work there; not in this range).

Verified: at `4d8d9a7`, `go test ./internal/cmd/ -run 'HandleHook|Opencode|OpenRouter|AuthOpencode'`, `go test ./internal/session/agent/harness/ -run Resolve_Opencode`, `go test ./internal/config/ -run 'Opencode|LoadRoleFrom_Opencode'` pass. Probed opencode **1.18.21** for isolation (oc1.1 notes below).

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

## oc1.3 wire-up (`4d8d9a7`)

Shared files. This is the production-install gate.

### P0 - Installing `feat/opencode-harness` as the live fleet binary drops Grok

**Location:** `internal/session/session.go` import block; branch vs `origin/feat/grok-stop-hook` and `origin/deploy/combined`

**Problem**
Live `session.go` (deploy/combined and feat/grok-stop-hook) blank-imports `claude`, `codex`, `generic`, **`grok`**. This branch's `session.go` is `claude`, `codex`, `generic`, **`opencode`**. There is no `internal/session/agent/harness/grok` on `feat/opencode-harness` (`git cat-file` fatal). merge-base: grok-stop-hook is **not** an ancestor of this branch.

The running fleet has grok-coder / grok-reviewer / grok-tg. `~/go/bin/h2` is a grok-capable build. A straight `go install` of this branch would make `harness.Resolve("grok")` fail (`unknown harness type`) and those agents would not relaunch.

oc1.3's blank import of opencode **next to claude/codex/generic** is the right pattern *on this branch*. Merging into the live tip should keep grok **and** add opencode (import-block conflict, easy). Installing this tip *instead of* live is not.

**Suggested fix**
Do **not** install `feat/opencode-harness` over the live binary. Rebase/merge onto the grok-capable deploy tip so `session.go` has all five blank imports, then install. Confirm `harness.Resolve` for `grok` and `opencode` both work in that tree.

---

### handle_hook `--event` vs Claude JSON — clean branch, Claude path preserved

**Location:** `internal/cmd/handle_hook.go`

**What changed**
- New `--event` flag. If set, that name is used and stdin is **not** required to be JSON.
- If unset, previous Claude path: unmarshal stdin for `hook_event_name` (now skipped when stdin is empty/whitespace).
- `sendHookEvent` / PreToolUse DCG / PermissionRequest still key off `eventName`. opencode names (`opencode.session.idle`) do not match those strings, so DCG/reviewer are not invoked.

**Claude / grok safety**
Claude settings.json is still `h2 handle-hook` with JSON on stdin, **no** `--event`. `TestHandleHook_SendsEventToAgent` still asserts `PreToolUse` + full payload forward + `{}` stdout. `TestHandleHook_DefaultsAgentFromH2Actor` covers `SessionStart` JSON. Invalid JSON still errors. Missing `hook_event_name` still errors (wording generalized; test updated to `strings.Contains`).

Grok does not use handle-hook (returns false from `HandleHookEvent`). Unaffected by this file.

`--event` overrides stdin `hook_event_name` if both are present. Claude never passes the flag. Plugin uses `--event` + empty stdin (`TestHandleHook_EventFlag_OpencodeIdle`).

**Not a finding.** This is the right generalization. Payload for `--event` with empty stdin is `[]byte("")`; opencode `HandleHookEvent` ignores payload. Fine.

---

### Blank import registration — correct on this branch

**Location:** `internal/session/session.go`, `internal/session/agent/harness/resolve_test.go`

`init()` in `opencode` registers `Names: []string{"opencode", "opencode_ai"}`, `DefaultCommand: "opencode"`. Blank-import next to the other harnesses is how Resolve finds it. `h2 run` → `agent_setup` imports `session` → init runs. `TestResolve_Opencode` covers canonical name and `opencode_ai` alias.

`Resolve`'s error string still says `supported: claude_code, codex, generic` (P3 stale text, not a runtime bug).

---

### P1 - First `h2 run --role opencode-coder` fails unless `opencode-config/default` already exists

**Location:** `internal/cmd/agent_setup.go` `validateHarnessConfigDirExists` vs `EnsureConfigDir`; `init.go` (no `opencode-config` scaffold)

**Problem**
`buildRoleRuntimeConfig` now sets prefix `<H2Dir>/opencode-config` (good — without this, isolation env would be empty). Then `validateHarnessConfigDirExists` **stats** `prefix/profile` and, if missing, errors:

```
profile "default" not found (missing …/opencode-config/default); … use 'h2 profile create default'
```

`EnsureConfigDir` (which `MkdirAll`s config + data + plugin) is only called **after** that check. `h2 init` scaffolds `claude-config/default` only. `h2 profile create` is Claude/codex-shaped and does not create `opencode-config`. The dir is created by `h2 auth opencode` (`MkdirAll`).

So the documented sequence `h2 auth opencode` then `h2 run` works. `h2 run` first does not, and the error points at the wrong command. Existing Claude agents are unchanged (their dirs already exist).

**Suggested fix**
Either: skip the exists-check for opencode and let `EnsureConfigDir` create the tree; or scaffold `opencode-config/default` in `h2 init` / `h2 auth`; and fix the error string to recommend `h2 auth opencode`.

Hardcoded prefix (no `GetOpencodeConfigPathPrefix` like Claude/Codex) is a P2 consistency nit — no role-level override.

---

### Role template

`ValidHarnessTypes` includes `opencode` / `opencode_ai`. `TestGetHarnessType_Opencode` + `TestLoadRoleFrom_OpencodeCoder`. Opinionated template `opencode-coder.yaml.tmpl` (`agent_harness: opencode`, Ox Alpha model, instructions only — AGENTS.md path). Embedded via `//go:embed templates/**`, so `h2 role` can instantiate it. Not in the minimal style (same as concierge.yaml). Fine.

`system_prompt` is catalogued as claude-only in `role_warnings.go`; the template correctly uses `instructions` only.

---

oc1.3 does **not** close the oc1.1/oc1.2 P1s (`pushState` still drops Idle; `BuildCommandEnvVars` / `opencodeAuthEnv` still missing XDG_CONFIG/STATE/CACHE and still append-not-replace). grok-coder has uncommitted `isolation.go` on `coder-oc-h2` — not reviewed here.

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
| role yaml / blank import / handle-hook `--event` / prefix | oc1.3: yes (see wire-up section) |

## Empirical notes (opencode 1.18.21)

- `auth.json` path in the bundle is `join(XDG_DATA_HOME, "opencode", "auth.json")` else `~/.local/share/opencode/auth.json`. **Not** under `OPENCODE_CONFIG_DIR`. Setting only `OPENCODE_CONFIG_DIR` leaks creds. This commit does set `XDG_DATA_HOME`.
- Local plugins auto-load from `OPENCODE_CONFIG_DIR/plugins/*.ts` (named or default export). Confirmed via `debug config --pure` listing `file://…/plugins/h2-bridge.ts`.
- Presence of a plugin triggers a background npm install in both `~/.config/opencode` and `OPENCODE_CONFIG_DIR` (observed `NpmInstallFailedError` when killed). Harmless if install is truly background for the TUI; still a reason to isolate `XDG_CONFIG_HOME`.

## Summary

**1 P0 (install), 5 P1, 4 P2, 3 P3.**

**Verdict**: **Not clean for a live-fleet install.** Wire-up of handle-hook / blank import / role **is correct on this branch**. Approved with revisions for the opencode feature itself.

**oc1.3 wire-up (the shared files):**
- `--event` is an additive branch. Claude JSON-on-stdin path is unchanged in behavior; existing handle-hook tests still pass. DCG / PermissionRequest only fire on those Claude event names.
- Blank import of `opencode` is the right registration. `opencode` and `opencode_ai` resolve.
- **Do not `go install` this branch over the live binary.** It has no grok harness; live `session.go` does. Merge onto the grok-capable tip first (P0).
- First opencode launch needs `opencode-config/default` already on disk (`h2 auth opencode`); `h2 run` alone errors with a misleading `h2 profile create` hint (P1).

**Still blocking oc1.4 signoff from oc1.1/oc1.2:**
1. `XDG_CONFIG_HOME` / `STATE` / `CACHE` unset — Path.config stays `~/.config/opencode`.
2. `opencodeAuthEnv` appends instead of replacing parent `XDG_*`.
3. Idle is droppable (`pushState` non-blocking); plugin hooks `message.updated`.
4. Tests for those paths.

Handle-hook tests + Resolve_Opencode + role load tests green at `4d8d9a7`. `make check` not run on the full tree.
