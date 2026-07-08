# Scoping: h2-u9w.6 revival (config root / profile separation)

**Author:** misc-fog · **Date:** 2026-07-07 · **Status:** scoping (no code changes)

**TL;DR:** The bead (written 2026-03-05, dormant ~4 months, assignee mild-cloud
not running) is **~80% already satisfied by intervening work**. Only a cosmetic
field rename remains, and that rename is **not** a hard prerequisite for the
task it "blocks" (h2-u9w.7). **Recommendation: NO-GO on reviving the bead
as-written** — close out the already-done parts, and either fold the residual
rename into h2-u9w.7's sweep or do it as a tiny standalone PR *after* h2-mzc
lands. h2-u9w.7 can start now.

## mild-cloud partial work: NONE

`git branch -a` and `git ls-remote --heads origin` show no `mild-cloud`/`u9w`
branch. Bead is `in_progress` but nothing was ever pushed — treat as unstarted.
Nothing to recover or rebase.

## The bead's 5 tasks vs. current code

| # | Bead task | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Remove `claude_code_config_path` / `codex_config_path` direct overrides | ✅ done/obsolete | Those fields don't exist — grep for non-`Prefix` variants is empty. |
| 2 | Rename `claude_code_config_path_prefix` → `claude_code_config_root` (+ codex) | ⬜ **PENDING** — the only real residual | `internal/config/role.go:273-274` (fields+yaml), getters `role.go:481,513`, `RuntimeConfig.HarnessConfigPathPrefix` `runtime_config.go:25`. |
| 3 | Store `harness_config_root` + `profile` separately in RuntimeConfig instead of pre-joined `harness_config_dir` | ✅ done | `RuntimeConfig.HarnessConfigPathPrefix` + `.Profile` already stored separately (`runtime_config.go:25-26`); `HarnessConfigDir()` is a **computed method** (`runtime_config.go:176`), not a stored pre-joined field. Design already exists; only the field *name* differs. |
| 4 | Add Claude session-log-path helper `<config_root>/<profile>/projects/<sanitize(cwd)>/<harness_session_id>.jsonl` | ✅ done | `claude.NativeLogPathSuffix(cwd, sessionID)` (`claude/harness.go:257`) → `projects/<sanitized-cwd>/<sessionID>.jsonl`; composed by `RuntimeConfig.NativeSessionLogPath()` (`runtime_config.go:189`) = `HarnessConfigDir()` + suffix = exactly the requested path. Already consumed by the Codex tailer (h2-up6) and `rotate.go`. |
| 5 | Fix `resolveSessionLogPath` in Claude harness to use it | ✅ obsolete | No `resolveSessionLogPath` function exists (grep confirms — only a stale mention in a `rotate.go` comment). Superseded by NativeLogPathSuffix/NativeSessionLogPath. |

**Net residual = a cosmetic field rename only** (`*_config_path_prefix` →
`*_config_root`, and `HarnessConfigPathPrefix` → `HarnessConfigRoot`).

## Does h2-u9w.6 actually block h2-u9w.7?

**No.** h2-u9w.7 (eliminate the `HarnessConfig` struct, make Session hold a
`*RuntimeConfig`, simplify `PrepareForLaunch`/`BuildCommandArgs` signatures)
depends on the **structural** root+profile separation — which already exists
(task 3) — **not** on the field *name*. h2-u9w.7 is functionally unblocked
today regardless of the rename.

## Blast radius of the rename (if done)

72 references across 15 files (`internal/config/role.go`, `runtime_config.go`,
`role_warnings.go`, `internal/cmd/{agent_setup,run,dry_run,rotate}.go`, plus
tests).

- **h2-mzc (coder-2-fog, merging to main now):** its new
  `internal/config/role_warnings.go` catalog uses the yaml strings
  `"claude_code_config_path_prefix"` / `"codex_config_path_prefix"` **and**
  `r.ClaudeCodeConfigPathPrefix` / `r.CodexConfigPathPrefix`. A rename edits
  those exact lines → **guaranteed conflict if both in flight**.
  `role_warnings.go` is **not on main yet**. → sequence any rename **after**
  h2-mzc lands.
- **h2-hsp (InputSender):** no overlap with config-path fields → **no conflict**.
- **h2-u9w.7:** heavily rewrites this same surface (Session fields,
  HarnessConfig). A standalone rename now is pure rebase churn against it.

## Recommendation

**NO-GO on reviving h2-u9w.6 as-written.** Concretely:

1. Close out tasks 1, 3, 4, 5 as already-satisfied (verified above).
2. Residual rename — pick one:
   - **(Preferred) Fold the rename into h2-u9w.7's sweep** and close/repoint
     h2-u9w.6. h2-u9w.7 already rewrites Session/HarnessConfig/PrepareForLaunch
     across these files, so the rename rides along at ~zero extra conflict cost
     and we avoid a churn-only standalone PR. **Start h2-u9w.7 now** — it isn't
     truly blocked.
   - **(Alt) Tiny standalone rename PR after h2-mzc lands** if you want the
     naming cleaned first. Low risk (mechanical, covered by existing tests) but
     pure churn.

## If you pick the rename — short plan

1. Wait for h2-mzc to land on main.
2. `git switch main && git pull && git switch -c <branch>`.
3. Rename Go identifiers: `ClaudeCodeConfigPathPrefix`→`ClaudeCodeConfigRoot`,
   `CodexConfigPathPrefix`→`CodexConfigRoot`,
   `HarnessConfigPathPrefix`→`HarnessConfigRoot`; getters
   `GetClaudeConfigPathPrefix`→`GetClaudeConfigRoot`, etc.
4. Rename yaml keys `claude_code_config_path_prefix`→`claude_code_config_root`,
   `codex_config_path_prefix`→`codex_config_root`; json tag
   `harness_config_path_prefix`→`harness_config_root`.
5. Update `role_warnings.go` catalog strings + all callers/tests; `make check`
   + `make test`.

### The one real risk (needs a Danny decision)

Step 4 is a **backwards-compat break**: existing role YAML files and any
persisted `RuntimeConfig` JSON on disk use the **old** keys. Renaming the
yaml/json tags silently ignores the old keys → a role that set
`claude_code_config_path_prefix` would fall back to the default config dir.
Options: (a) add back-compat aliases in `Role.UnmarshalYAML` /
`RuntimeConfig`; or (b) accept the break with a migration note. Per h2's
"no fallbacks unless instructed" norm this leans (b), but user-authored role
files may warrant an alias. **This is the only non-mechanical part — flag to
Danny before implementing.**
