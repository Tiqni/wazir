# Wazir PR Watch — Observe + Report (Phase 1) Design

**Status:** Design approved; ready for implementation plan.
**Date:** 2026-06-17
**Branch:** `pr-watch-observe-report`

## Goal

Once Wazir opens a PR and moves a card to **PR Review**, it currently goes silent
(`resolver.go`: `PRReview → ActNone`). This phase makes Wazir *observe* the PR's
review and CI state and *report* it back onto the issue, and surface trouble on the
board — without ever changing code or merging.

This is **phase 1 of two**. Phase 1 = observe + report (this spec). Phase 2 (a
separate, later spec) = an auto-fix loop that re-enters the worktree to address
review feedback / fix failing CI. Phase 1 is deliberately built so phase 2 layers
cleanly on top of it (a "Failed card that has a PR" is the phase-2 rework trigger).

## Scope (locked during brainstorming)

- **Behavior:** always post a comment summarizing the state; **move the card to
  `Failed`** on red CI or a changes-requested review; **green/approved is
  informational only** (no move — that's the human's merge gate; Wazir never merges).
- **Signals (decision-grade only):** a review submitted as **approved** or
  **changes_requested**; CI (the check suite) **completed** as success/failure.
  Plain "commented" reviews, individual inline comments, and in-progress/neutral/
  skipped checks are ignored. (The richer signals are phase-2 auto-fix *input*.)
- **Trigger:** webhook-driven (`pull_request_review`, `check_suite`), with an
  authoritative REST re-fetch of current state on each trigger — the event is only
  a "something changed on this PR, go look" wake-up, never trusted as the payload.

### Out of scope (phase 2 or later)

- Re-entering the worktree to make code changes (auto-fix loop).
- Surfacing individual inline review comments.
- **Acting on plain PR conversation comments** (e.g. "the linter is failing").
  These are free-text human direction with no structured status, and the only useful
  response — actually doing the work — *is* the auto-fix loop. They are the natural
  **phase-2 rework trigger**; that spec decides the trigger semantics (any comment? a
  `@wazir`-style command? only changes-requested reviews?) and must route them through
  the same PR-index reverse-map built here (a PR comment's issue node ID is the *PR's*,
  not the card's).
- Moving a card to `Done` (implies merge — human-gated).
- Polling / startup reconcile for events missed while the daemon is down. (Webhook
  delivery can be missed if `wazir serve` is down; acceptable for phase 1.)
- Loop-prevention for the bot's *own* re-pushes triggering CI events — phase 1 never
  pushes, so it cannot trigger its own `check_suite`/`pull_request_review` events.

## Architecture — port responsibilities

The two-port, provider-agnostic core (CLAUDE.md rule 1) is preserved.
`internal/orchestrator/imports_test.go` must still pass.

### CodeForge owns PR/CI reads

Reading PR reviews and CI status is VCS-forge territory (about the PR and its head
commit, not the kanban surface), so the **`CodeForge` port** (`internal/forge`) gains
one read method:

```go
PRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error)
```

returning a **domain** struct — no `go-github` types cross the port:

```go
type PRStatus struct {
    ReviewDecision string   // "approved" | "changes_requested" | "review_required" | ""
    CIConclusion   string   // "success" | "failure" | "pending" | ""  ("" = no checks present)
    FailingChecks  []string // names of failed check-runs, for the comment body
    HeadSHA        string   // the commit the checks ran against
}
```

The github forge implements it via REST (the client it already holds for `OpenPR`):
- `pulls/{n}/reviews` → reduce to the **latest** review state per reviewer, then to an
  overall decision (any reviewer's latest = `changes_requested` ⇒ `changes_requested`;
  else ≥1 `approved` and none requesting changes ⇒ `approved`; else `review_required`/`""`).
- `commits/{sha}/check-runs` for the PR head SHA → any `completed`+`failure`/`timed_out`/
  `cancelled` ⇒ `failure` (collect names); all `completed`+`success`/`neutral`/`skipped`
  ⇒ `success`; any still in progress ⇒ `pending`; no check-runs ⇒ `""`.

### Board owns event recognition

The **`Board` port** keeps its existing surface (`ParseEvent`, `PostComment`,
`MoveTo`, `GetCard`). It only learns to recognize two new webhook types and map them
to a card (below). All provider mapping stays inside `internal/board/github`.

### PR → card mapping (store PR-index)

PR webhooks carry a PR number, not the issue node ID the system keys on. When the
worker opens the PR it knows **both** (the card's issue node ID and `pr.GetNumber()`),
so it writes a small reverse index `repo#prNumber → issueNodeID` into the store.
`ParseEvent` reverse-looks-up through it. O(1), no branch-name parsing, no extra API
call. A PR webhook with no index entry is not a Wazir card → `EventIgnore`.

**Guard existing `issue_comment` handling.** Conversation comments on a PR also arrive
as `issue_comment` events, but their `issue` node ID is the *PR's*, not the card's
issue — today's handler would map them to a bogus card ID. Phase 1 adds a guard:
`IssueCommentEvent` where `e.GetIssue().IsPullRequest()` (i.e. `PullRequestLinks != nil`)
→ `EventIgnore`. (Phase-2 rework will instead route these through the PR-index above.)

## Data flow

1. **Inbound.** App is subscribed to `pull_request_review` + `check_suite`. The
   existing receiver passes the raw webhook to `Board.ParseEvent`, which:
   - validates the signature, dedupes on delivery id (existing),
   - filters by repo allow-list + bot sender (existing loop-prevention),
   - reverse-maps `repo#prNumber → issueNodeID` via the store PR-index,
   - emits a domain `Event{Kind, CardID, Repo, Dedup}` with one of two new kinds:
     **`EventReviewSubmitted`** or **`EventChecksCompleted`**. No PR detail rides on
     the event — it is purely a "go look at this PR" signal.
2. **Enqueue.** The receiver enqueues keyed by card; the queue serializes per-card
   work (existing), so a review event and a CI event for the same card can't race.
3. **Resolve.** `Worker.Process` loads the card + record and calls the resolver:
   ```go
   case board.PhasePRReview:
       if ev.Kind == board.EventReviewSubmitted || ev.Kind == board.EventChecksCompleted {
           return Decision{ActReport}
       }
       return Decision{ActNone}
   ```
4. **Act (`reportPhase`).**
   - `prNumber := rec.PRNumber` (persisted at PR-open),
   - `status, err := forge.PRStatus(ctx, card.Repo, prNumber)` — fetches **current**
     review decision + CI conclusion (ignores the noisy event payload),
   - on `err`: **soft-fail** (log a warning, return nil — no comment, no move, no
     delta write); the next webhook/re-delivery retries,
   - **delta check** against `rec.LastReviewState` / `rec.LastCIConclusion`: compare
     the freshly-fetched `ReviewDecision`/`CIConclusion` to each persisted value;
     if neither differs → no-op (collapses CI's chatty re-deliveries). Both event
     kinds run this same path (we always re-fetch *both* dimensions), so a review
     event and a CI event are handled identically.
   - if either changed → `PostComment(summary)` (marker-stamped), where `summary`
     contains a line for **each** dimension that changed (so a single re-fetch that
     finds both a new review decision and a new CI conclusion posts one comment with
     both lines); and if the *current* state has
     `ReviewDecision == "changes_requested"` **or** `CIConclusion == "failure"` →
     `MoveTo(PhaseFailed)`,
   - persist the new `Last*` state **only after** a successful comment + move.
5. **Terminal.** Approved / green → comment only (no move). Once moved to `Failed`,
   later PR events for that card resolve to `ActNone` (it has left `PRReview`) —
   Wazir goes quiet until a human re-engages (phase-2 territory).

### Report comment content (marker-stamped `<!-- wazir -->`)

- CI failure: `❌ CI failed on <shortSHA>: <name1>, <name2> failing. Moving to Failed.`
- Changes requested: `🔄 @<reviewer> requested changes. Moving to Failed.`
- Approved: `✅ @<reviewer> approved.`
- CI success: `✅ CI passed on <shortSHA>.`

(The reviewer login is best-effort from `PRStatus`; if unavailable, omit the `@…`.)

## State & store changes

`CardRecord` (`internal/store/store.go`) gains three fields — same `project/repo/item`
keying, no new keying:

```go
PRNumber         int    // the open PR's number; captured at OpenPR; enables PRStatus + the PR-index
LastReviewState  string // "" | "approved" | "changes_requested" — delta state, suppresses re-comments
LastCIConclusion string // "" | "success" | "failure" | "pending" — delta state
```

`Store` gains a PR-index (new bbolt bucket; mirrored in the memory impl):

```go
PutPRIndex(repo string, prNumber int, issueNodeID string) error
GetPRIndex(repo string, prNumber int) (issueNodeID string, ok bool, err error)
```

Key: `repo + "#" + strconv.Itoa(prNumber)`. The index entry is intentionally **not**
cleaned up when a card leaves PR Review — it is tiny, one issue maps to one PR (no
accumulation), and phase-2 will want it.

`OpenPR`'s signature changes to return the number it already has in hand:

```go
OpenPR(ctx, repo, branch, base, title, body string) (prURL string, prNumber int, err error)
```

In `executePhase`, after a successful open the worker:
1. persists `rec.PRNumber = prNumber` (alongside the existing branch/worktree coords),
2. writes `store.PutPRIndex(card.Repo, prNumber, card.ID)`,

then posts the "Opened PR" comment and moves to PR Review as today.

## Error handling, loop prevention & idempotency

- **Read failures are soft.** A `PRStatus` API hiccup must not drop the card to
  `Failed` — the error is *ours*, not the card's. `reportPhase` logs and returns nil;
  no comment, no move, no delta write. This is the one deliberate departure from the
  worker's usual "error → `fail()` → move to Failed" path.
- **Comment/move failures are hard.** `PostComment`/`MoveTo` errors are real board
  I/O — they propagate and `Process` runs the normal `fail()` path. Delta state is
  persisted **only after** a successful comment + move, so a mid-flight failure
  re-reports on retry rather than being silently swallowed.
- **Loop prevention (existing layers).**
  1. The bot's `MoveTo(Failed)` emits a `projects_v2_item` event → filtered by
     `bot_login` sender.
  2. The bot's `PostComment` emits an `issue_comment` event → filtered by `bot_login`
     + the `<!-- wazir -->` marker (all report comments are marker-stamped).
  3. The new PR/CI events concern reviews and checks; phase 1 never submits reviews
     or pushes commits, so it cannot trigger its own `pull_request_review`/
     `check_suite` events.
- **Idempotency / delta (two layers).** Delivery-id dedup kills exact re-deliveries
  (existing); the `Last*` delta check kills *semantically* redundant events (CI
  failing → re-run → still failing fires two `check_suite` events with different
  delivery ids but the same conclusion → the second no-ops). Repeated and out-of-order
  webhooks are safe.

## Configuration & operational prerequisites

- **App webhook subscriptions:** enable `Pull request review` and `Check suite`
  events in the GitHub App settings.
- **App permission bump:** reading GitHub Actions results (check-runs) requires
  **Checks: read** — an org-admin permission upgrade (re-consent). Reading reviews
  needs Pull requests: read, which the App already has from `OpenPR`.
- No new `wazir.yaml` keys — the feature is always on for cards in PR Review (the App
  subscription/permission is the real gate). (YAGNI: a config toggle can be added
  later if needed.)

## Testing strategy

All unit-level, no network or credentials — fits the existing `go test ./...`
discipline.

- **`internal/forge/github` — `PRStatus` (httptest, existing `BaseURL`-redirect
  pattern).** Serve canned JSON for `pulls/{n}/reviews` and `commits/{sha}/check-runs`:
  - multiple reviews from one reviewer → only the **latest** counts
    (approve-then-request-changes ⇒ `changes_requested`);
  - mixed check-runs ⇒ `failure` + failing names collected; all success ⇒ `success`;
    no checks ⇒ `""`; any in-progress ⇒ `pending`.
- **`internal/board/github` — `ParseEvent` (table-driven over `testdata/` fixtures).**
  Add `pull_request_review.json` + `check_suite.json` fixtures. Assert:
  - mapped to the right `CardID` via a **seeded PR-index** ⇒ correct new `EventKind`;
  - no index entry ⇒ `EventIgnore`;
  - bot-sender / disallowed-repo ⇒ `EventIgnore`.
- **`internal/store` — PR-index + new fields.** Round-trip `PutPRIndex`/`GetPRIndex`
  (hit + miss); `CardRecord` persists `PRNumber`/`LastReviewState`/`LastCIConclusion`
  (bbolt **and** memory impls).
- **`internal/orchestrator/resolver_test` —** `PhasePRReview` + `EventReviewSubmitted`/
  `EventChecksCompleted` ⇒ `ActReport`; `PhasePRReview` + other kinds ⇒ `ActNone`;
  non-PRReview phases unaffected.
- **`internal/orchestrator/worker_test` — `reportPhase` with a fake forge** returning
  `PRStatus` variants:
  - `changes_requested` ⇒ comment **and** `MoveTo(Failed)`;
  - CI `failure` ⇒ comment + move, failing names in the body;
  - `approved` / CI `success` ⇒ comment, **no** move;
  - **delta suppression:** same status twice ⇒ exactly one comment/move;
  - `PRStatus` error ⇒ no comment, no move, no delta write, returns nil (soft).
- **`internal/orchestrator/imports_test.go`** continues to pass — the new domain
  `PRStatus` keeps the core provider-free.

No integration-test changes required; the build-tagged live provisioning test is
untouched.

## File map

```
internal/forge/forge.go          # + PRStatus method on CodeForge; + PRStatus domain struct
internal/forge/github/forge.go   # implement PRStatus (REST); OpenPR returns prNumber
internal/board/board.go          # + EventReviewSubmitted, EventChecksCompleted kinds
internal/board/github/parse_event.go  # handle pull_request_review + check_suite; PR-index reverse map
internal/board/github/testdata/  # + pull_request_review.json, check_suite.json fixtures
internal/store/store.go          # CardRecord += 3 fields; + PutPRIndex/GetPRIndex on Store
internal/store/bbolt.go          # add bucketPRIndex to the buckets slice; PutPRIndex/GetPRIndex
internal/store/memory.go         # PR-index map + PutPRIndex/GetPRIndex in the memory impl
internal/orchestrator/decision.go  # + ActReport in the Action enum (+ String())
internal/orchestrator/resolver.go  # PRReview + new events → Decision{ActReport}
internal/orchestrator/worker.go    # + reportPhase; execute() ActReport case; executePhase persists PRNumber + PR-index
```
