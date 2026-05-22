---
name: babysit-pr
description: Schedule yourself to wake up every 5 minutes and shepherd an open PR — fix failing CI, address review-bot findings (push fixes and let the re-review pass close stale comments; resolve false positives and human-accepted threads yourself), reply to mechanical human comments with fixes, and flag architecture-level human comments back to the user. Goal is zero unresolved threads (each unresolved thread blocks mergeability). Exits when the PR is green and review is clean. NEVER merges the PR.
user-invocable: true
allowed-tools: Bash Read Write Edit Grep Glob Task
argument-hint: "[pr-number-or-url] [--review-bot @name]"
---

# Babysit PR

You have an open PR that needs ongoing attention as CI runs, review bots post comments, and humans drop occasional feedback. Rather than the user (or you) actively babysitting it, you'll **self-schedule** wakeups every 5 minutes for up to ~20 fires and process whatever's new each time.

This skill ends when the PR is green and review is clean. **It NEVER merges the PR** — see the bottom of this doc.

## Inputs

- `$0`: PR number or full URL (e.g. `123` or `https://github.com/org/repo/pull/123`).
- `$1` (optional): `--review-bot @name` — the review bot you'll re-tag for follow-up reviews (e.g. `@bugbot`, `@claude`, `@greptile`, `@codex`). If omitted, infer from the bots that have already commented on the PR.

## Prerequisites: the PAT this skill expects

The `gh` CLI must be authenticated with a token whose grants match what this skill actually does — and, more importantly, *don't* permit what it must never do (merge).

**Required grants:**

- **`pull_requests: write`** — open/edit/comment on PRs, push commits to the branch, reply to review threads.
- **`contents: read`** — clone, fetch, read files (for the local fix/push cycle).
- **`actions: read`** — fetch workflow run logs to diagnose CI failures.
- **`metadata: read`** — always required.

**Must NOT have:**

- **`contents: write`** — this would let the token push to the base branch and merge the PR. The skill's hard rule is "never merge"; remove the capability and the rule is harder to break by accident.
- **`administration: write`** — bypassing branch protection, changing merge settings.

Grant `pull_requests: write` (not `contents: write`) is the meaningful split: the skill needs to push commits to a feature branch and edit PR comments, both of which are covered by `pull_requests: write`. Merging requires `contents: write` on the base, which the skill never needs.

**A known limitation regardless of grants:** GitHub's `resolveReviewThread` GraphQL mutation rejects fine-grained PATs by token *type*, returning `"Resource not accessible by personal access token"` even when `viewerCanResolve: true` is reported for the thread and the token has admin perms on the repo. Other PR mutations (e.g. `addComment`, `addPullRequestReviewComment`) work fine — only the resolve/unresolve mutations are blocked. If you hit this, post the inline disposition reply anyway and ask the user to click resolve in the UI; do not pretend the thread is closed.

## Phase 1: Set up the wakeup schedule

You'll schedule yourself to receive a wakeup message every 5 minutes, capped so it can't run forever. Get your own agent name and schedule on it:

```bash
AGENT=$(h2 whoami)

h2 schedule add "$AGENT" \
    --name "babysit-pr-<pr-number>" \
    --rrule "FREQ=MINUTELY;INTERVAL=5;COUNT=20" \
    --from babysit-pr \
    --message "babysit-pr: check PR <pr-number>"
```

- `FREQ=MINUTELY;INTERVAL=5` — every 5 minutes.
- `COUNT=20` — at most 20 firings (~1h40m of total elapsed clock time). The cap is a safety belt so a stuck PR doesn't loop forever.
- `--message` injects into your own PTY each wakeup. When you see it arrive, repeat **Phase 2** below.

If you reach the cap without converging, surface that to the user and stop. Do not re-arm.

## Phase 2: What to do on each wakeup

Each 5-minute wakeup, do these steps in order. Skip what's obviously not relevant (no new comments, no failing checks). Don't re-do work you've already done in a previous tick.

### Step 2a — Check CI

```bash
gh pr checks <pr-number>
```

- All green → continue to 2b.
- Failing → run the failing job, read the error, **push a fix**. Most CI failures are: type errors, lint errors, broken unit tests. Fix them locally, run the local equivalent (`make lint`, `make typecheck`, `make test` — whatever the repo uses), then `git push`. End turn — let the next wakeup re-check.

**Fine-grained personal access tokens can't read the GitHub Checks API.** If `gh` is authenticated with a fine-grained PAT (the default for most workstations — `gh auth status` will show the token type), every code path that touches Checks returns 403 `Resource not accessible by personal access token`. The token grants are not the problem; the Checks API simply isn't exposed to PATs. This will not start working mid-loop. Stop retrying and switch to the fallbacks below.

Commands that **fail** under a PAT — avoid these for CI status:

- `gh pr checks <pr>` — GraphQL `statusCheckRollup` path.
- `gh pr view <pr> --json statusCheckRollup` (and any other `--json` field whose name contains `statusCheck` / `checkRuns`). The rest of the JSON still comes back, so it looks like it worked — but stderr is full of per-context 403s and the field you wanted is empty.
- `gh api repos/<org>/<repo>/commits/<sha>/check-runs` — REST Checks API.
- `gh api repos/<org>/<repo>/check-suites/...` — same family.

Commands that **work** under a PAT — use these instead:

```bash
# Actions workflow runs for the branch (covers anything running in GitHub Actions).
gh run list --branch <branch> --limit 40 \
    --json status,conclusion,name,headSha \
    --jq '[.[] | select(.headSha == "<head-sha>")] |
          group_by(.name) |
          map({name: .[0].name, status: .[0].status, conclusion: .[0].conclusion})'

# Legacy combined commit status (covers external CIs reporting via the Statuses API:
# CircleCI, Vercel, Buildkite, etc.).
gh api "repos/<org>/<repo>/commits/<head-sha>/status" \
    --jq '{state, statuses: [.statuses[] | {context, state, description}]}'

# Failing-job logs — also works under a PAT.
gh run view <run-id> --log-failed
```

Use both queries: `gh run list` for Actions, the combined-status call for external CIs. Together they reconstruct the same picture `gh pr checks` would have shown.

Everything else you need for this skill works fine under a PAT — `gh pr view --json reviews,comments,body,...` (just not the check fields), `gh pr comment`, `gh api .../pulls/<n>/comments`, `gh api .../issues/<n>/comments`, `gh run view`, `gh run list`. Only the Checks API path is blocked.

### Step 2b — Pull review comments

```bash
gh pr view <pr-number> --json reviews,comments
gh api "repos/<org>/<repo>/pulls/<pr-number>/comments"
```

Group comments by author. The relevant authors are:

- **Review bots** — `@bugbot`, `@claude` (Claude Code review), `@codex`, `@greptile`, etc. Anything that looks programmatic.
- **Humans** — everyone else.

### Step 2c — Handle review-bot comments

**The goal: zero unresolved threads.** Every open thread on a PR — bot or human — blocks merge under most branch-protection configs and clutters reviewer attention even when it doesn't. "Addressed" is not the same as "resolved." A thread with your reply "fixed in abc123" but no resolved-checkmark is still an open thread.

For each bot comment that isn't already resolved:

1. **Read it carefully.** Look at the file/line, understand what the bot is claiming.

2. **Pick a disposition:**

   - **Legit, fixable** (bot is right, change the code): write the fix, push it. **Don't comment "Ok to resolve" yet** — wait for the bot's re-review (Step 2e). The happy path is: push fix → `@<bot> review` → bot reads new code → bot resolves the stale thread automatically. Posting your own "Ok to resolve" before the bot gets a chance defeats the verification step.

   - **False positive** (bot misunderstood, claim doesn't apply, code is correct as-is): post a comment that *starts* with **`Ok to resolve`** followed by the one-line reason. Then resolve via `resolveReviewThread` if your auth permits; otherwise the user clicks resolve.

   - **Accepted / won't fix** (bot's concern is valid but a human has decided not to change the code — out of scope, deferred, tradeoff already considered): post a comment starting with **`Ok to resolve`** and citing the human decision ("Per @<user>'s disposition above, accepting <X> because <Y>"). Then resolve / hand off to the user.

3. **When the bot comes back after your fix (Step 2e), look at each thread again:**

   - Bot resolved it → done.
   - Bot left it open and didn't comment on it → bot saw your fix and didn't re-flag it. You may post **`Ok to resolve — addressed in <commit>`** and resolve.
   - Bot left it open and posted a follow-up finding → not done. Either push more fix, or — if you and the bot disagree about what "fixed" means — flag the user and stop pinging the bot. Don't ping-pong.

4. **The "Ok to resolve" lead matters.** When the user is the one clicking resolve (either because your auth can't, or because they're double-checking your work), they're scanning many threads at once. A comment that opens with `Ok to resolve — <reason>` is parseable in one glance. A comment that opens with "Fixed in c1cd83cdc via channel cap wrap_inbound…" makes them read the whole reasoning before they know whether to click. Lead with the verdict.

5. **If `resolveReviewThread` returns FORBIDDEN:** this is the PAT-rejection limitation called out in the Prerequisites section — not a missing grant. Post the `Ok to resolve` reply anyway so the verdict is on-record. In your end-of-turn summary list the thread IDs and ask the user to click resolve. Don't pretend the thread is closed — it isn't until someone clicks the button.

6. **Why this ordering matters:** resolving (or posting "Ok to resolve") *before* the bot has re-reviewed short-circuits the safety net — the whole point of the bot pass is independent verification. Leaving threads open *after* the bot has had its turn and disposition is decided is what creates a stuck PR. The middle path — push, re-tag, wait one cycle for the bot, then `Ok to resolve` whatever the bot didn't handle — is the one that actually drives the unresolved count to zero.

### Step 2d — Handle human comments

For each human comment that isn't already addressed:

- **Mechanical / specific** (rename this var, extract this helper, add a null check, fix this typo): push the fix, reply to the comment explaining what you pushed (`gh pr comment` or `gh api` to reply inline). Don't ask permission for the kind of change a careful reader would just accept.

- **Architectural / "let's think about this"** (let's reconsider the approach, what if we did X entirely differently, this whole component should be rewritten, why are we even doing this): **do not start a rewrite**. This skill is not the place for fundamental scope changes. Reply to the comment along the lines of:

  > Waiting for user to discuss this — @<user-handle> let me know how you'd like to proceed.

  Then continue processing other comments. If this thread is the only outstanding item, you'll still end the turn and wake up later — the user may have responded by then.

### Step 2e — Re-request bot reviews

If you pushed any fixes in 2c or 2d, re-request the relevant bot reviews so they see the new code.

**The trigger comment MUST stand alone.** Most review bots scan only for comments whose entire body is `@<bot> review` (sometimes with a trailing newline). They will NOT pick up a trigger appended to the end of a longer status update or summary comment — the bot's matcher sees the surrounding prose, decides it's not a command, and ignores it.

Always send the trigger as its own `gh pr comment` call, with nothing else in the body:

```bash
gh pr comment <pr-number> --body "@bugbot review"
gh pr comment <pr-number> --body "@claude review"
# etc. for whichever bots are reviewing this PR
```

Don't write:

```bash
# WRONG — bot won't see the trigger
gh pr comment <pr-number> --body "Pushed fix in abc123. @cursor review"
```

Post your summary as one comment, then the trigger as a separate, single-line comment.

Each bot has its own trigger phrase — the convention is `@<botname> review` for the most common ones. If you're not sure, look at how previous turns triggered the bot on this PR (and confirm it actually fired a new review afterward — silent failures from buried triggers look identical to bot down-time).

### Step 2f — End turn

Output a one-paragraph summary of what you did this tick (CI status, comments addressed, fixes pushed, bots re-tagged), then end the turn. The next scheduled wakeup will arrive in 5 minutes.

## Phase 3: Exit when clean

The PR is "clean" when:

- All CI checks pass.
- **Zero unresolved review threads.** Not "all addressed," not "all replied to" — actually marked resolved. GitHub's mergeable status checks the resolved flag, not whether a reply exists. If your PAT can't resolve them, the user has to click — surface that explicitly and don't claim the PR is clean while threads are still open.
- No outstanding human comments are waiting on a decision from the user (architectural questions you flagged for them).

When you confirm all of the above:

```bash
# Find the schedule you registered
h2 schedule list "$(h2 whoami)"

# Remove it by ID
h2 schedule remove "$(h2 whoami)" <schedule-id>
```

Post a final summary on the PR (and to the user via `h2 send <user> "..."`) noting the PR is ready for human review/merge. Then stop. **Do not merge.**

## Hard rule: DO NOT MERGE THE PR

**Under no circumstances does this skill merge the PR — not even if every check is green, every bot has approved, every comment is resolved, and the author asks nicely.**

- Don't run `gh pr merge`.
- Don't run `gh pr ready` if it would auto-merge.
- Don't trigger any merge bot or merge-queue join.
- Don't bypass branch protection.

The merge decision belongs to a human. This skill exists to get the PR INTO a mergeable state and STOP. The final "ship it" click is someone else's responsibility, by design — it forces a deliberate human pause before code lands on the trunk.

If you find yourself about to type `gh pr merge`, stop. That's not what this skill does.

## What requires judgment

1. **False positive vs. legit (Step 2c).** Bots get this wrong sometimes — both false positives and false-cleans. Read the actual code. If the bot's claim doesn't match what the code does, mark it false positive with a one-line reason. If you're not sure, err toward "legit" — pushing a small refactor is cheaper than having a real bug slip through.

2. **Mechanical vs. architectural (Step 2d).** A "rename this variable" is mechanical. A "let's consider whether this whole abstraction is right" is architectural. When in doubt, treat it as architectural and flag for the user — the cost of waiting one extra cycle is small; the cost of starting a wrong rewrite is large.

3. **When to stop early.** If the same comment keeps coming back after you've pushed multiple fixes the bot disagrees with, stop and flag the user. You're likely in a loop where you and the bot disagree about what "fixed" means — escalate rather than ping-pong.

4. **What "re-request review" means for each bot.** Conventions differ:
   - Some bots auto-re-review on any push (no comment needed).
   - Some require an explicit `@<bot> review` comment.
   - Some have custom slash commands (`/review`, `/recheck`).
   Check past comments on this PR to see how the bot was triggered before, and use that.

## Anti-patterns

- **Posting "Ok to resolve" before the bot has had a chance to re-review.** You're skipping the verification step. Push, re-tag, wait one cycle, *then* resolve or hand off what the bot didn't pick up.
- **Burying the verdict.** A long, well-reasoned inline reply that doesn't start with "Ok to resolve" forces the human clicking through to read the whole thing before they know what action to take. Lead with `Ok to resolve` (or `Pushed fix in <commit> — awaiting bot re-review` if you're still mid-cycle) so the verdict is the first thing they see.
- **Leaving "human accepted / won't fix" threads open indefinitely** waiting for a bot resolution that will never come. The bot only resolves when code changes. If the disposition is "we're not changing this," you have to lead with `Ok to resolve` and the cited human decision.
- **Confusing "replied" with "resolved."** A thread with your inline disposition reply is still an open thread until someone clicks resolve. Mergeability and reviewer attention both key on the resolved flag, not on whether a reply exists.
- **Pinging the bot repeatedly without taking other action.** If you've tagged `@<bot> review` and ~10 minutes pass with no response, the bot may simply be slow or down — don't keep re-tagging. Move on to whatever else needs doing; the bot will catch up or it won't.
- **Starting an architectural rewrite from a single review comment.** That's a much bigger commitment than this skill is sized for. Flag the user.
- **Reaching the COUNT cap without converging and silently re-arming.** If you ran 20 cycles and the PR still isn't clean, something's stuck (loop with bot, flaky test, waiting on user). Stop and report — don't add another schedule.
- **Editing the merge target.** All commits go on the PR branch. Don't touch `main` (or whatever the base is). The merge happens via the UI by a human, after this skill has ended.
- **Merging the PR.** See above. Don't.
