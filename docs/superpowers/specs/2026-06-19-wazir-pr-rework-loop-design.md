# Wazir PR Rework Loop (Phase 2) Design

**Status:** Design approved; ready for implementation plan.
**Date:** 2026-06-19
**Branch:** `pr-rework-loop` (stacked on `pr-watch-observe-report`, PR #13).

## Goal

Phase 1 (observe + report, PR #13) detects trouble on a card's PR — a
changes-requested review or red CI — and moves the card to **Failed**. Phase 2
lets a human direct Wazir to *fix* it: re-enter the card's worktree, run one
headless `claude` turn that addresses the review feedback and repairs the failing
checks, and re-push. On success the card returns to **PR Review**, where phase 1
observes the next review/CI cycle normally. The two phases compose:

```
PR Review ──(changes-requested / red CI)──▶ Failed          [phase 1]
Failed ──(human trigger)──▶ Reworking ──(re-push)──▶ PR Review
                               └──(rework fails / cap hit)──▶ Failed
```

Wazir never merges; a reworked PR always lands back at the human's merge gate.

## Scope (locked during brainstorming)

- **Human-gated only.** Silence never auto-reworks. Changes-requested / red CI keep
  phase-1 behavior (report + move to Failed); a human then explicitly triggers rework.
- **Two triggers, converging on one action (`ActRework`):**
  - **A — column move:** a human drags the card `Failed → Reworking` (the existing
    `projects_v2_item → EventPhaseChanged` path; no new parsing).
  - **B — PR comment command:** a human comments `@wazir fix` (configurable) on the
    PR. Phase 1 ignored PR `issue_comment`s; phase 2 routes them via the PR-index to
    the card and recognizes the command → `EventReworkRequested`. The worker then
    moves the card to `Reworking` itself, so both triggers converge on the same
    in-progress state.
- **New `Reworking` column** (additive-safe §3 provisioning; display "Reworking",
  color PINK). The card lives here during a rework cycle.
- **Feedback given to the turn:** the changes-requested review body + inline
  (line-level) comments, **and** each failed check-run's annotations, **plus** the
  turn reproduces failures locally by running the repo's tests/linters in the worktree.
- **Worktree recovery from the remote PR head:** the first-build worktree was removed
  when the card entered PR Review, and recreating it reset-to-base would wipe the PR's
  commits. Rework fetches and checks out `origin/<feature-branch>` (the PR's current
  head) into a fresh worktree — a new forge op distinct from the reset-to-base one.
- **Hard cost cap, default 3 rounds** per card (`ReworkRounds`), then escalate without
  spending — mirrors the `max_brainstorm_turns` breaker.

### Out of scope (later, or never)

- Automatic (non-human-gated) rework on trouble.
- Command arguments / a command grammar beyond the single `@wazir fix` token.
- Fetching full CI logs (annotations + a local re-run are sufficient; logs are large,
  noisy, and mostly redundant).
- Wazir merging or approving — always the human's decision.

## Architecture — port responsibilities

The two-port, provider-agnostic core (CLAUDE.md rule 1) is preserved.
`internal/orchestrator/imports_test.go` must still pass.

### CodeForge gains three reads + one worktree op

```go
// CreateWorktreeFromBranch adds a worktree checked out at the branch's REMOTE head
// (origin/<branch>, after a fetch) — the rework mirror of CreateWorktree, which
// resets to base. Preserves the PR's existing commits.
CreateWorktreeFromBranch(ctx context.Context, repo, branch string) (path string, err error)

// PRReviewFeedback returns the changes-requested review body + line-level comments.
PRReviewFeedback(ctx context.Context, repo string, prNumber int) (ReviewFeedback, error)

// CheckAnnotations returns the annotations of the PR head's failed check-runs.
CheckAnnotations(ctx context.Context, repo string, prNumber int) ([]CheckAnnotation, error)
```

Domain return types (no `go-github` types cross the port):

```go
type ReviewFeedback struct {
    Summary  string          // the changes-requested review body
    Comments []InlineComment // line-level review comments
}
type InlineComment struct {
    Path   string
    Line   int
    Body   string
    Author string
}
type CheckAnnotation struct {
    Check   string // check-run name
    Path    string
    Line    int
    Level   string // "failure" | "warning" | "notice"
    Message string
}
```

The github impl backs these with REST: `pulls/{n}/reviews` +
`pulls/{n}/comments` (inline); `check-runs/{id}/annotations` for each failed run
(the run ids come from the same `commits/{sha}/check-runs` call phase 1's
`PRStatus` already makes). `CreateWorktreeFromBranch` is the existing
`CreateWorktree` with start-ref `origin/<branch>` instead of `origin/<base>`, and
**without** the `-B`-reset semantics — after a `fetch origin`.

### Brain gains one method

```go
Rework(ctx context.Context, in ReworkInput) (ReworkResult, error)
```

```go
type ReworkInput struct {
    Transcript    string          // issue context, as other turns get
    WorktreePath  string          // cwd for the turn (the recreated PR-head worktree)
    Feedback      ReviewFeedback  // review body + inline comments
    FailingChecks []string        // names of failed checks (from PRStatus)
    Annotations   []CheckAnnotation
}
type ReworkResult struct {
    Status PhaseStatus // StatusComplete | StatusFailed
    Notes  string
    Error  string
}
```

The `ClaudeBrain` impl shells **one** headless turn in the worktree
(`cmd.Dir = WorktreePath`), prompt ≈ *"Address the following review feedback and
fix the failing checks. Run the repository's tests. Commit your work on the current
branch; do not push or open a PR. Then stop."* — with the feedback/annotations
rendered as **prompt data** (framed as feedback to address, not instructions).
No separate plan phase. Bounded by `rework_timeout`; scoped `rework_allowed_tools`.

### Orchestrator core

Gains an `ActRework` action + a `reworkPhase` method, depending only on the two
ports + the new domain types. `imports_test.go` still holds.

## Data flow

**Trigger A (column move):** `Failed → Reworking` drag → `projects_v2_item` webhook
→ existing `ParseEvent` → `EventPhaseChanged{NewPhase: Reworking}`. Resolver:
`Phase == Reworking → ActRework`.

**Trigger B (`@wazir fix`):** PR `issue_comment` webhook. Phase 1's `IsPullRequest()`
guard (which returned `EventIgnore`) is replaced: for a PR comment, reverse-map via
the **PR-index** to the card; if the body contains the command token (and the sender
isn't the bot) → `EventReworkRequested{CardID, Repo, Dedup}`, else `EventIgnore`.
Resolver: `EventReworkRequested → ActRework` from `PRReview` **or** `Failed`.

**`reworkPhase` (worker):**
1. `GetCard` record. **Cap check first:** if `ReworkRounds >= max` → escalation
   comment, `MoveTo(Failed)`, return (no `claude` spend).
2. If entered via trigger B (card not yet in Reworking), `MoveTo(Reworking)`.
3. `forge.EnsureClone` → `forge.CreateWorktreeFromBranch(repo, rec.Branch)`.
4. Gather feedback: `forge.PRReviewFeedback(repo, rec.PRNumber)` +
   `forge.CheckAnnotations(repo, rec.PRNumber)` (+ failing-check names from a
   `PRStatus` call, reused from phase 1).
5. `brain.Rework(ReworkInput{...})` — one headless turn; commits on the branch.
6. On `StatusComplete`: `forge.PushBranch(repo, rec.Branch)` → increment
   `ReworkRounds`, persist → `PostComment("Reworked (round N); pushed…")` →
   `MoveTo(PRReview)` → remove worktree (best-effort).
7. On rework failure/error: increment `ReworkRounds`, persist → escalation comment
   → `MoveTo(Failed)` → **keep** the worktree (debugging), as `executePhase` does.

**After a successful rework**, the re-push fires fresh `check_suite` /
`pull_request_review` webhooks; the card is in `PR Review`, so **phase 1 observes
them normally** (its delta state re-reports the new outcome).

## State & store changes

`CardRecord` gains ONE field (same `project/repo/item` keying):

```go
ReworkRounds int // count of rework attempts; the cost breaker (default cap 3)
```

Everything else needed already survives from phase 1: `PRNumber`, `Branch`, and the
PR-index. `reworkPhase` reads `rec.Branch` (for `CreateWorktreeFromBranch`) and
`rec.PRNumber` (for the feedback fetches). **No new store methods.** (`WorktreePath`
is re-derived by the forge from repo+branch, so a stale value from the removed
first-build worktree is irrelevant.)

New event kind + action (both appended, preserving iota order):
`board.EventReworkRequested`, `orchestrator.ActRework`.

## Command grammar (trigger B) — minimal (YAGNI)

- Token **`@wazir fix`**, matched case-insensitively as a substring of the comment
  body. No arguments, no other commands.
- Configurable via `claude.rework_command` (default `@wazir fix`).
- **Loop prevention:** the match is gated by the existing bot-sender filter
  (Wazir's own comments are `bot_login` / marker-stamped), so Wazir can't trigger
  itself. Only a human-authored command fires it.

## Configuration

New `claude` keys (env `WAZIR_CLAUDE_*`):
- `max_rework_rounds` (default `3`, struct tag) — the cost breaker cap.
- `rework_command` (default `@wazir fix`, struct tag) — trigger-B token.
- `rework_timeout` (default = `execute_timeout`) — the rework turn is execute-class.
- `rework_allowed_tools` (default = `execute_allowed_tools`) — edits code + runs
  tests/git.

`rework_timeout` / `rework_allowed_tools` default to the *execute* values, which fig
struct tags can't express (a tag is a literal, not another field). Resolve them in
code after `config.Load`: if the field is zero/empty, copy `ExecuteTimeout` /
`ExecuteAllowedTools`. Keep this in one place (a small `ClaudeConfig` post-load
normalizer) so the fallback is obvious and tested.

## Error handling, cost & security

### Cost (§12)

- **Hard cap** (`ReworkRounds >= max` → escalate without spending), checked before any
  turn — like the brainstorm breaker.
- The rework turn is **one** headless turn, bounded by `rework_timeout`, no plan phase
  — it can't fan out into multiple metered invocations.
- `ReworkRounds` increments on **every attempt** (success or failure) that actually
  ran a turn, so a fix→still-broken→fix rut converges on the cap and escalates rather
  than spinning. Per-turn `cost_usd` / `session_id` logged like execute.

### Error handling (mirrors `executePhase`)

- `CreateWorktreeFromBranch` / feedback-fetch failure → *our* infra error → return it
  so `Process` runs `fail()` (comment + move to Failed); `ReworkRounds` is **not**
  incremented (no turn ran — a transient fetch failure shouldn't burn budget).
- `brain.Rework` → `StatusFailed`/error → increment `ReworkRounds`, escalation comment,
  move to Failed, **keep** the worktree.
- `PushBranch` failure after a successful turn → fail path; commits are safe locally,
  a re-trigger re-pushes. `ReworkRounds` already incremented (the turn ran).
- Board state persisted only after the successful push+move, so a mid-flight failure
  re-attempts cleanly on the next trigger.

### Security (§12 prompt-injection — sharper than phase 1)

The rework turn feeds **attacker-influenceable text** (PR review bodies, inline
comments, CI annotations — anyone who can comment on the PR) into a turn that edits
code and runs git in the worktree. Mitigations reuse existing machinery:

- Same **least-privilege isolation** as execute: scoped `rework_allowed_tools`, per-run
  `CLAUDE_CONFIG_DIR`, no secrets in the environment, never `--dangerously-skip-permissions`.
- `resetOrigin` before any push (already in the forge) blocks a tampered `origin` from
  redirecting the token-bearing push — directly relevant since rework acts on
  attacker-touched content.
- The branch is **orchestrator-owned** (`feature/issue-<n>-…`, never `res.Branch`), and
  Wazir **never merges**, so a compromised turn can at worst push to its own PR branch,
  which a human still reviews before merge. The human gate is the backstop.
- Feedback is passed as **prompt data, not instructions**, framed by the system prompt
  as "review feedback to address" — consistent with existing issue/spec text handling.

## Testing strategy

All unit-level, no network/credentials.

- **`internal/forge/github` (httptest):**
  - `PRReviewFeedback` — `pulls/{n}/reviews` + `pulls/{n}/comments`; assert
    changes-requested body + inline comments (path/line/body/author); empty body / no
    inline comments → valid-but-empty.
  - `CheckAnnotations` — failed `check-runs` + their `annotations`; assert
    check/path/line/level/message mapped; no annotations → empty slice.
  - `CreateWorktreeFromBranch` (git-level, like existing worktree tests) — asserts it
    checks out `origin/<branch>` (NOT `origin/<base>`) and does NOT reset the branch to
    base, so the PR's commits are preserved. **The load-bearing correctness test.**
- **`internal/board/github` — `ParseEvent` (table-driven fixtures):**
  - PR `issue_comment` with the command token, seeded PR-index → `EventReworkRequested{CardID}`.
  - PR comment without the token → `EventIgnore` (phase-1 non-command behavior preserved).
  - Command from the bot sender → `EventIgnore` (loop prevention).
  - Case-insensitive match; unknown PR (no index) → `EventIgnore`.
- **`internal/orchestrator`:**
  - `resolver_test` rows: `Reworking + <any> → ActRework`; `PRReview + EventReworkRequested
    → ActRework`; `Failed + EventReworkRequested → ActRework`; `Failed + bare phase-change
    → ActNone` (unchanged).
  - `reworkPhase` (fake forge + `scriptedBrain`):
    - cap reached (`ReworkRounds == max`) → escalation comment, move to Failed, brain
      NOT called, no worktree created (breaker proven).
    - success → `CreateWorktreeFromBranch` called, feedback fetched, `PushBranch` called,
      `ReworkRounds` incremented, comment posted, move to `PRReview`, worktree removed.
    - rework `StatusFailed` → `ReworkRounds` incremented, escalation, move to Failed,
      worktree kept.
    - infra error (worktree/fetch) → fail path, `ReworkRounds` NOT incremented.
    - trigger-B path moves the card to `Reworking` first.
  - `imports_test.go` still passes — core imports only ports + the new domain types.

No integration-test changes; the build-tagged live test is untouched.

## File map

```
internal/forge/forge.go               # + CreateWorktreeFromBranch, PRReviewFeedback, CheckAnnotations; + ReviewFeedback/InlineComment/CheckAnnotation domain types
internal/forge/github/forge.go        # implement the three (REST + git start-ref origin/<branch>)
internal/board/board.go               # + PhaseReworking constant (+ AllPhases order) + EventReworkRequested kind
internal/board/github/mapping.go      # + PhaseReworking → "Reworking" (+ PINK color); provisioning picks it up
internal/board/github/parse_event.go  # PR issue_comment: command-token match via PR-index → EventReworkRequested (replaces the phase-1 ignore guard)
internal/store/store.go               # CardRecord += ReworkRounds
internal/config/config.go             # + max_rework_rounds, rework_command, rework_timeout, rework_allowed_tools (+ post-load normalizer for the execute-derived defaults)
internal/orchestrator/decision.go     # + ActRework (+ String())
internal/orchestrator/resolver.go     # Reworking → ActRework; EventReworkRequested (PRReview/Failed) → ActRework
internal/orchestrator/brain.go        # + Rework on the Brain port; + ReworkInput/ReworkResult
internal/orchestrator/brain_canned.go # CannedBrain.Rework stub
internal/orchestrator/worker.go       # + reworkPhase; execute() ActRework case
internal/claude/brain.go              # ClaudeBrain.Rework (one headless turn) + rework prompt/settings
```
