# Wazir — Board-Driven Dev Loop (GitHub Projects × Claude Code + Superpowers)

> Hand this document to Claude Code. It is written as a PRD + technical design + phased
> implementation plan. If you use Superpowers, feed it to `/superpowers:brainstorm` first so
> the model can ask clarifying questions before planning.
>
> **Stack: Go.** &nbsp;**Module:** `github.com/EmadMokhtar/wazir` &nbsp;**Binary:** `wazir` (daemon: `wazird`).
>
> *Name:* **Wazir** (Arabic *wazīr*, وزير) — the vizier: the chief minister who administered and
> orchestrated the affairs of state on the ruler's behalf, dispatching work, overseeing operations, and
> reporting up. The fitting namesake for an orchestrator that shepherds work through stages and reports
> back. (The same role the ancient Egyptians called the *tjaty*.)

---

## 1. Goal

Build Wazir, a small, long-running **orchestrator service** (in Go) that turns a GitHub Projects v2
board into the control surface for an autonomous-but-human-gated software workflow. A human writes an
idea or bug as a card; Wazir drives it through brainstorming → spec → planning → execution; the human
approves at each gate by interacting with the card (comments / column moves); Wazir opens a PR and
posts the link back.

Wazir does **not** reimplement Superpowers. It invokes Claude Code (with the Superpowers plugin)
headlessly as the "brain" for each phase, and owns all deterministic GitHub state changes itself.

---

## 2. Design principles

1. **The board is the source of truth.** Phase = the card's `Status` field value. The orchestrator
   derives what to do from the board + comment thread on every event.
2. **Separation of concerns.** The orchestrator owns *all* provider I/O (create comment, rewrite spec,
   move column, open PR) deterministically through the `Board`/`CodeForge` ports. Claude is invoked
   only to *reason* and returns structured text. Never let the model move columns or open PRs directly
   — too unreliable.
3. **Human gates are explicit.** The system only advances a card past a review gate on an explicit
   human signal (a label, an approval comment, or a column move). Silence never auto-advances.
4. **Idempotent & lockable.** Webhooks fire repeatedly and out of order. Every action is keyed to an
   event id / last-processed comment id, and work on a given issue is serialized.
5. **Isolated execution.** Each card's build runs in its own git worktree so multiple cards can
   progress in parallel without colliding.
6. **Provider-agnostic core, two ports.** The orchestrator core (state machine, context builder,
   Claude runner) depends only on two Go interfaces — a `Board` port (the kanban surface: cards,
   columns, comments, provisioning) and a `CodeForge` port (the VCS surface: clone, branch, push, open
   PR). GitHub implements both; another provider (e.g. Linear for the board, GitLab for the forge) can
   be added without touching the core. The interfaces speak the domain vocabulary (Card, Column,
   Comment, ApprovalSignal), never provider concepts (node IDs, GraphQL, `projects_v2_item`).

---

## 3. The state machine

The Project's `Status` single-select field defines the columns. Recommended values:

| Column            | Owner        | Meaning / trigger                                                        |
|-------------------|--------------|--------------------------------------------------------------------------|
| `Inbox`           | Human        | New idea/bug. Human writes the card.                                     |
| `Brainstorming`   | Orchestrator | Picked up; running a brainstorm turn.                                    |
| `Awaiting Answers`| Human        | Orchestrator posted clarifying questions; waiting for a human reply.     |
| `Spec Review`     | Human        | Brainstorm decided it's clear; spec written to the issue body.           |
| `Planning`        | Orchestrator | Approved spec; running `write-plan`, creating worktree.                  |
| `Building`        | Orchestrator | Running `execute-plan` in the worktree.                                  |
| `PR Review`       | Human        | Execution done; PR opened and link posted.                               |
| `Done`            | Human        | PR merged (native GitHub).                                               |
| `Blocked`/`Failed`| Orchestrator | A phase errored; needs human attention. Always have a failure column.    |

**Transitions (the 9 steps mapped):**

```
Inbox ──(human moves to Brainstorming OR orchestrator auto-picks)──▶ Brainstorming
Brainstorming ──Claude: needs answers──▶ Awaiting Answers   (post questions as comment)
Awaiting Answers ──human comments──▶ Brainstorming          (re-run with updated thread)   [loop steps 3–5]
Brainstorming ──Claude: spec ready──▶ Spec Review           (rewrite issue body = spec)    [step 6]
Spec Review ──human comments (revisions)──▶ Spec Review      (re-run, adjust spec)          [step 7]
Spec Review ──human approves (label/column)──▶ Planning                                     [step 7→8]
Planning ──worktree created, plan written──▶ Building                                       [step 8]
Building ──execution complete──▶ PR Review                  (open PR, post link)            [step 9]
PR Review ──human merges──▶ Done
any phase ──error──▶ Blocked/Failed                         (post error comment)
```

---

## 4. Architecture

```
                 GitHub
   ┌─────────────────────────────────────┐
   │ Issues · Comments · Projects v2      │
   │ Webhooks: issues, issue_comment,     │
   │           projects_v2_item           │
   └───────────────┬─────────────────────┘
                   │ webhook (HTTPS)
                   ▼
        ┌───────────────────────┐
        │  Webhook Receiver      │  net/http; ValidatePayload + ParseWebHook; dedupe; enqueue
        └──────────┬────────────┘
                   ▼
        ┌───────────────────────┐
        │  Per-issue queue       │  goroutine + keyed mutex: serialize per issue,
        │  (goroutines/channels) │  run different issues concurrently
        └──────────┬────────────┘
                   ▼
        ┌───────────────────────┐
        │  Orchestrator Worker   │
        │  1. resolve phase      │◀─── State Resolver (reads board + thread)
        │  2. build context      │◀─── Context Builder (issue + comments → transcript)
        │  3. run Claude phase   │───▶ Claude Runner (os/exec → `claude -p` + Superpowers)
        │  4. parse output       │
        │  5. write to GitHub    │───▶ GitHub Writer (go-github REST + githubv4 GraphQL)
        │  6. move column        │
        └──────────┬────────────┘
                   │
                   ▼
        ┌───────────────────────┐
        │  Worktree Manager      │  git worktree per issue (os/exec)
        └───────────────────────┘

        ┌───────────────────────┐
        │  SQLite store          │  issue→worktree map, session ids, last comment id,
        │                        │  processed delivery ids (idempotency), locks
        └───────────────────────┘
```

### 4.1 Board scope (one board, multi-repo-ready)

**v1 targets exactly one Projects v2 board.** A single board is the whole point of the design — one
control surface to watch — and it keeps everything simple: one `wazir bootstrap` run, one cached set of node
IDs, and trivial webhook routing (an event either belongs to the configured project or is dropped).

Two things to keep straight, because they're independent:

- **Board count ≠ repo count.** Projects **v2** are owned at the user/org level and can hold issues and
  PRs from *many* repos. Choosing one board does **not** lock you to one repo — you can add cards from
  several repos to the same board. (This differs from deprecated "classic" projects, which were
  repo-scoped.) The board is the control surface; repos are just where code lives.
- **Issue/comment webhooks come from repos, not the board.** The board only emits `projects_v2_item`
  events. So even with one board spanning multiple repos, the GitHub App must be installed on **each
  repo** whose issues you want to act on, and each card must carry which repo it belongs to so the
  Worktree Manager clones/branches the right one.

**Multi-board is explicitly out of scope for v1** (see §13) but must be *cheap to add later*. The one
thing to build in now, because it's nearly free:

> **Convention — carry identifiers on every record and every write.** Every store row and every GitHub
> write/column-move is keyed by `(project_id, repo, item)`, not just `item`, even though there's only
> one project today. Going multi-board later becomes "turn `PROJECT_NUMBER` into a list and add a
> routing switch in the Webhook Receiver" instead of a schema migration. **Do not build the routing
> now — just leave the seam:** the config holds a single project, but the data and write paths are
> already project-aware.

Concretely: the Webhook Receiver drops any `projects_v2_item` event whose project node id ≠ the
configured project; the store schema (§7) includes `project_id` and `repo`; and the GitHub Writer
(§8.5) takes the project/field/option IDs as parameters rather than reading a global.

### 4.2 Provider abstraction (two ports)

The core orchestrator must not import any GitHub package. It depends on two interfaces, and GitHub is
just the first implementation of each. Splitting into **two** ports (rather than one big "GitHub"
interface) is deliberate: a board and a code host are different concerns and may be different vendors
(e.g. Linear board + GitHub forge), and "open a PR" is not a kanban concept.

**Port A — `Board`** (the kanban control surface):

```go
// package board — domain types, no provider leakage.

type Phase string // Inbox, Brainstorming, AwaitingAnswers, SpecReview, Planning, Building, PRReview, Done, Failed

type Card struct {
    ID        string   // opaque, provider-defined (a GitHub issue node id, a Linear issue id, …)
    Repo      string   // "owner/name" — which forge repo this card's work targets
    Title     string
    Body      string   // the idea, later the spec
    Phase     Phase
    Comments  []Comment
}

type Comment struct {
    ID       string
    Author   string
    IsBot    bool     // provider sets this; core uses it to skip self-events
    Body     string
    Created  time.Time
}

// ApprovalSignal is how a human says "advance" — abstracted over labels / column moves / etc.
type ApprovalSignal int
const ( SignalNone ApprovalSignal = iota; SignalApproveSpec; SignalRequestRevision )

type Board interface {
    // Provisioning (point 1): create the board + the Phase columns if absent; idempotent.
    EnsureProvisioned(ctx context.Context, spec BoardSpec) error

    // Read
    GetCard(ctx context.Context, cardID string) (Card, error)
    ListCards(ctx context.Context, phase Phase) ([]Card, error)

    // Write (deterministic; the orchestrator owns these)
    PostComment(ctx context.Context, cardID, body string) error
    SetBody(ctx context.Context, cardID, markdown string) error      // step 6: spec → card body
    MoveTo(ctx context.Context, cardID string, phase Phase) error     // column move

    // Event normalization: turn a raw provider webhook into a domain event.
    ParseEvent(headers map[string]string, payload []byte) (Event, error)
}

type BoardSpec struct {
    Name    string
    Columns []Phase  // desired Status options, in order
}

type Event struct {
    Kind     EventKind  // CardCreated, CommentAdded, PhaseChanged, ApprovalGiven
    CardID   string
    Comment  *Comment        // for CommentAdded
    NewPhase Phase           // for PhaseChanged
    Signal   ApprovalSignal  // for ApprovalGiven
    Dedup    string          // provider event/delivery id for idempotency
}
```

**Port B — `CodeForge`** (the VCS surface: where worktrees, branches, and PRs live):

```go
// package forge
type CodeForge interface {
    Clone(ctx context.Context, repo, dest string) error
    CreateWorktree(ctx context.Context, repo, branch, path string) error
    RemoveWorktree(ctx context.Context, path string) error
    PushBranch(ctx context.Context, repo, branch string) error
    OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, err error)
}
```

**What this buys you / the rules:**

- The `internal/orchestrator` core imports only `board` and `forge`. To add Linear, write
  `internal/board/linear` implementing `Board`; the forge can stay GitHub. No core changes.
- The mapping from a provider's native column names ↔ domain `Phase` lives **inside** the
  implementation (e.g. the GitHub impl caches the `Status` field id + option ids and translates).
- `IsBot` and `Dedup` are normalized by the implementation so the core's loop-prevention and
  idempotency logic is provider-independent.
- Honest-abstraction test: you should be able to write a `board/memory` fake implementing `Board` and
  run the *entire* state machine against it with no GitHub at all. Make this fake part of M1's tests.

> Note: with two ports, a "card" and the "repo" it targets are linked by `Card.Repo`. For the GitHub
> impl they often coincide (issue and PR in the same repo); for a Linear-board + GitHub-forge setup
> they're genuinely different systems, which is exactly why the ports are separate.

---

## 5. Tech stack (Go; libraries swappable)

> **M0 settled these swappable choices:** config = `kkyr/fig`, logging = `go.uber.org/zap`,
> CLI = `spf13/cobra`, store = `go.etcd.io/bbolt`. The bullets below keep the original rationale;
> where M0 diverged it is noted inline.

- **Language/runtime:** Go 1.24+ (the `go` directive is `1.24.0`; the floor is `testing.T.Chdir` in
  the config tests. oauth2 was dropped from the code so it no longer forces 1.25 — see Auth below.)
- **GitHub (implements both ports):**
  - `google/go-github` — REST for issues, comments, PRs, and webhook parsing
    (`github.ValidatePayload`, `github.ParseWebHook`).
  - `shurcooL/githubv4` — typed GraphQL client for **Projects v2**: provisioning
    (`createProjectV2`, single-select option management) and moves (REST can't touch v2 cards).
  - Auth: `bradleyfalzon/ghinstallation` for GitHub App installation tokens (preferred, scaffolded).
    The M0 PAT path is a hand-rolled bearer-token `http.RoundTripper` (no `golang.org/x/oauth2` — it
    was only setting one header, and pulled in an unused `cloud.google.com/go` transitive + forced Go 1.25).
- **Claude:** **No official Go SDK** — invoke the `claude` CLI headlessly via `os/exec`
  (`claude -p ... --output-format json`) and unmarshal the JSON envelope. Requires the `claude`
  binary **and the Superpowers plugin** installed on the box.
- **Persistence:** **`go.etcd.io/bbolt`** (chosen for M0 — dependency-free embedded KV). SQLite via
  `modernc.org/sqlite` (pure Go, no cgo, behind `database/sql`) remains an option if relational
  queries are wanted later.
- **Concurrency / queue:** native goroutines + channels. Serialize per issue with a keyed mutex map
  (`map[string]*sync.Mutex` guarded by a mutex) or one worker goroutine per issue id. No Redis/broker
  needed for a side project.
- **Webhook server:** `net/http` stdlib (optionally a router like `go-chi/chi`) — M1.
- **Config:** **`kkyr/fig`** — an optional `wazir.yaml` (nested sections) with `WAZIR_<SECTION>_<FIELD>`
  env overrides; runs env-only when no file is present. (Replaces the original `caarlos0/env` plan.)
- **CLI:** **`spf13/cobra`** — `provision` / `bootstrap` / `card` commands with persistent
  `--config` / `--log-level` / `--log-format` flags.
- **Logging:** **`go.uber.org/zap`** (structured; `console` or `json`).
- **Hosting:** a single cheap always-on box (the orchestrator must hold the repo clone + worktrees and
  shell out to `claude`). A laptop is fine for v1.

### 5.1 Suggested project layout

```
cmd/wazir/main.go            // wire deps, start http server + worker pool (binary: wazir / wazird)
cmd/wazir/provision.go       // `wazir provision` subcommand → calls Board.EnsureProvisioned (point 1)
internal/config/             // env config
internal/board/              // the Board port (interface + domain types: Card, Comment, Phase, Event)
internal/board/github/       // GitHub impl of Board: go-github + githubv4, provisioning, event parse
internal/board/memory/       // in-memory fake Board for testing the full state machine (no network)
internal/forge/              // the CodeForge port (interface)
internal/forge/github/       // GitHub impl of CodeForge: clone/worktree/push/PR
internal/claude/             // os/exec the claude CLI; parse JSON envelope + phase contract
internal/orchestrator/       // state resolver, context builder, phase dispatch — imports only board+forge
internal/store/              // sqlite/bbolt: issues, deliveries, runs, locks
internal/queue/              // per-issue serialized goroutine pool
```

> Dependency rule: `internal/orchestrator` may import `internal/board` and `internal/forge` (the
> interfaces) but **never** `internal/board/github` or any provider package. Providers are injected in
> `main.go`. This is what keeps the abstraction real rather than decorative.

---

## 6. Provisioning & setup (`wazir provision` and `wazir bootstrap`)

There are two distinct one-time operations. Keep them as separate subcommands so you can re-run either
independently; both must be **idempotent**.

### 6.1 `wazir provision` — create the board if asked (point 1)

`Board.EnsureProvisioned(ctx, BoardSpec{Name, Columns})` creates the board and reconciles its columns
to the desired `Phase` set. For the GitHub impl this is all GraphQL:

1. If no project with `Name` exists for the owner, `createProjectV2` to make one; otherwise reuse it.
2. **Reconcile the `Status` field.** A freshly created Projects v2 board ships with a *default*
   `Status` single-select field pre-populated with `Todo` / `In Progress` / `Done`. So this step is
   not "create from blank" — it's: read the existing options, **add** any missing columns from §3,
   and decide a policy for the defaults (keep, rename, or remove). Recommended: add all nine §3
   columns, then optionally prune defaults you don't use. Never blindly recreate — that's how you get
   duplicates.
3. Enable the built-in **"Auto-add to project"** workflow so new issues land in `Inbox`
   (or have the orchestrator add cards itself on `issues.opened`).
4. Persist the resulting project id + field id + option-id-per-phase to the store (the cache the rest
   of the system reads).

Idempotency contract: running `wazir provision` twice converges to the same board with no duplicate
fields or options.

### 6.2 `wazir bootstrap` — cache IDs for an existing board

When the human already made the board by hand, skip creation and just read+cache the IDs the writer
needs on every move (in Go, via the typed `shurcooL/githubv4` client):
```graphql
query($org:String!, $number:Int!) {
  organization(login:$org) {            # or user(login:$user)
    projectV2(number:$number) {
      id
      field(name:"Status") {
        ... on ProjectV2SingleSelectField { id options { id name } }
      }
    }
  }
}
```
(`wazir provision` calls this same reconcile-and-cache logic after creating the board, so they share
code.)

### 6.3 Manual prerequisites (not automatable via API)

- Register a GitHub App (or PAT) with permissions: **Issues** (read/write), **Pull requests**
  (read/write), **Contents** (read/write), **Projects** (read/write), and subscribe to webhook events:
  `issues`, `issue_comment`, `projects_v2_item`. Install it on each repo (§4.1).
- Create the human-signal labels (`spec-approved`, `needs-revision`) — or have `wazir provision` create
  these too via the REST labels API, since that part *is* automatable.

---

## 7. Data model (SQLite)

```
issues(
  issue_node_id TEXT PRIMARY KEY,
  project_id TEXT,              -- board node id (§4.1: project-aware now, single value for v1)
  repo TEXT,                    -- "owner/name" the card's issue lives in (multi-repo-ready)
  issue_number INTEGER,
  project_item_id TEXT,         -- node id of the card in the project
  phase TEXT,                   -- mirror of Status, for sanity checks
  worktree_path TEXT,
  branch TEXT,
  claude_session_id TEXT,       -- optional: resume context across turns
  last_processed_comment_id INTEGER,
  lock_owner TEXT, lock_expires_at INTEGER
)
deliveries(delivery_id TEXT PRIMARY KEY, processed_at INTEGER)   -- webhook idempotency
runs(id INTEGER PK, issue_node_id TEXT, phase TEXT, status TEXT, cost_usd REAL, started_at, ended_at)
```

---

## 8. Component specs

### 8.1 Webhook Receiver
- HTTP handler is provider-agnostic: it hands the raw headers + body to `board.ParseEvent`, which
  returns a normalized domain `Event` (or an error / "ignore"). The GitHub impl does the signature
  verification (`github.ValidatePayload`), type-switch (`*github.IssuesEvent`,
  `*github.IssueCommentEvent`, `*github.ProjectV2ItemEvent`), `IsBot` tagging, and `Dedup` extraction
  *inside* `ParseEvent` — none of that leaks into the receiver.
- Drop events whose project node id ≠ the configured project (§4.1).
- Dedupe on `Event.Dedup`; record in `deliveries`.
- Drop `Event`s where the originating comment/move `IsBot` (loop prevention).
- Enqueue surviving events by `CardID`.

### 8.2 State Resolver
- Given an issue, read current `Status` and the full comment thread.
- Decide the action purely from (phase, who acted, what's new since `last_processed_comment_id`).
- Acquire the per-issue lock before acting; release with `defer`.

### 8.3 Context Builder
- Produce a single transcript the model can reason over:
  - Issue title + body.
  - Comment thread, each tagged `HUMAN:` or `SYSTEM:` (orchestrator/Claude). Exclude machine markers.
  - The current phase and the specific instruction for this turn.
- The **board thread is the source of truth.** Optionally resume the stored `claude_session_id`
  (`claude -p --resume <id> ...`) as an *optimization* when the last turn was recent; otherwise
  reconstruct from the thread.

### 8.4 Claude Runner (the brain)
- Runs in an environment where **Superpowers is installed**; a `CLAUDE.md` /
  `--append-system-prompt` injects the "headless contract" (see §9).
- There is no Go SDK, so shell out to the CLI and parse the JSON envelope:
  ```go
  cmd := exec.CommandContext(ctx, "claude", "-p", prompt,
      "--output-format", "json",
      "--permission-mode", "acceptEdits",          // non-interactive; scope tightly (see §12)
      "--allowedTools", "Read,Edit,Bash(git:*)",   // verify flags against installed version
  )
  cmd.Dir = worktreePath                            // for plan/execute phases
  var stderr bytes.Buffer; cmd.Stderr = &stderr
  out, err := cmd.Output()
  // 1) json.Unmarshal the CLI envelope (final result text, session id, cost)
  // 2) extract the fenced ```json phase-contract block from the result text (see §9)
  ```
- Persist `session_id` and the run cost to `runs`. Always use `exec.CommandContext` with a timeout and
  capture stderr for diagnostics.
- One invocation = one phase turn. Headless is single-turn and exits — the Go worker is the supervisor
  that strings turns together.

### 8.5 GitHub `Board` implementation (deterministic writes)
This is `internal/board/github` — the concrete implementation of the `Board` port (§4.2). The
orchestrator calls the *interface* methods; everything below is implementation detail hidden behind
them.
- REST (comments, issue body) via `google/go-github`; provisioning + column moves via
  `shurcooL/githubv4`.
- `PostComment` — questions, status notes, PR link, error reports.
- `SetBody` — step 6, the spec becomes the issue body. Keep the original idea in a collapsed
  `<details>` block at the top for history.
- `MoveTo(phase)` translates the domain `Phase` → the cached `Status` option id, then
  `updateProjectV2ItemFieldValue`:
  ```go
  var m struct {
    UpdateProjectV2ItemFieldValue struct {
      ProjectV2Item struct{ ID githubv4.ID }
    } `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
  }
  input := githubv4.UpdateProjectV2ItemFieldValueInput{
    ProjectID: projectID, ItemID: itemID, FieldID: statusFieldID,
    Value: githubv4.ProjectV2FieldValue{
      SingleSelectOptionID: githubv4.NewString(githubv4.String(optionID)),
    },
  }
  err := client.Mutate(ctx, &m, input, nil)
  ```
- `EnsureProvisioned` — see §6.1 (create board + reconcile columns, idempotent).
- `ParseEvent` — normalize a raw GitHub webhook into a domain `Event`, setting `IsBot` (author ==
  `BOT_LOGIN`) and `Dedup` (the `X-GitHub-Delivery` header) so the core stays provider-agnostic.
- Stamp bot-authored content with a hidden marker (e.g. `<!-- orchestrator -->`) so `ParseEvent` can
  reliably flag its own events.

### 8.6 GitHub `CodeForge` implementation + Worktree Manager
This is `internal/forge/github` — the concrete `CodeForge` port (§4.2).
- Maintain one clone per repo on the box.
- `CreateWorktree` on entering `Planning`:
  `git worktree add ../wt/issue-<n> -b feature/issue-<n>-<slug> origin/main` (via `os/exec`).
- Plan/execute phases run with `cmd.Dir` set to the worktree.
- `OpenPR` — go-github `PullRequests.Create`; the orchestrator then posts the returned URL via the
  `Board` port.
- `RemoveWorktree` on PR merge / card to `Done`: `git worktree remove` and prune.

### 8.7 Idempotency & concurrency
- **In-process:** a keyed mutex (one mutex per issue id) serializes turns for the same card while
  different cards run concurrently in their own goroutines.
- **Across restarts:** a per-issue advisory lock in SQLite with a TTL (so a crashed worker self-heals).
- Track `last_processed_comment_id` so re-delivered comment events don't re-trigger a turn.

---

## 9. Phase behavior & output contracts

The model must return **machine-parseable** signals so the orchestrator knows what to do. Enforce via
the headless system prompt: *"End every response with a single fenced `json` block matching the schema
for the current phase. Put all human-facing prose inside the JSON fields, not outside the block."*
In Go, locate the last fenced ```json block in the result text and `json.Unmarshal` it.

**Brainstorm turn** — invoke Superpowers brainstorming with the transcript:
```json
{ "phase": "brainstorm",
  "status": "needs_answers" | "spec_ready",
  "questions": ["...", "..."],          // when needs_answers
  "spec_markdown": "..." }              // when spec_ready
```
- `needs_answers` → post `questions` as a comment, move card to `Awaiting Answers`.
- `spec_ready` → replace the issue body with `spec_markdown`, move card to `Spec Review`.

**Spec revision turn** (human commented in Spec Review without approving) → re-run brainstorm with the
new comment; same contract; stay in `Spec Review` after updating the body.

**Approval** (human adds `spec-approved` label or moves card to `Planning`) → enter Planning.

**Plan turn** — invoke `/superpowers:write-plan` against the worktree using the approved spec:
```json
{ "phase":"plan", "status":"plan_ready"|"failed", "plan_path":"...", "summary":"...", "error":"..." }
```

**Execute turn** — invoke `/superpowers:execute-plan` in the worktree:
```json
{ "phase":"execute", "status":"complete"|"failed",
  "branch":"...", "commits":[...], "test_summary":"...", "notes":"...", "error":"..." }
```
- success → orchestrator commits/pushes the branch, opens the PR, posts the link, moves to `PR Review`.
- failure → post error report, move to `Blocked/Failed`.

> Superpowers' skills auto-trigger from natural-language intent, but in headless runs invoke the slash
> commands explicitly in the prompt for determinism, and pass the transcript/spec as context.

---

## 10. Implementation milestones

Build in independently shippable, testable slices (fits a TDD/Superpowers flow).

- **M0 — Ports + GitHub plumbing + provisioning.** Define the `Board` and `CodeForge` interfaces and
  domain types (§4.2) *first*. Implement `internal/board/github` (`PostComment`, `SetBody`, `MoveTo`,
  `ParseEvent`) and `internal/forge/github` (`OpenPR`). Implement `EnsureProvisioned` and the
  `wazir provision` + `wazir bootstrap` subcommands (§6). Table-driven tests against a scratch repo.
  *Demo: `wazir provision` creates the board with the §3 columns from scratch (idempotently), then a CLI
  command moves a card and comments on it through the `Board` interface.*
- **M1 — Webhook receiver + idempotency + queue + in-memory Board.** Provider-agnostic receiver calling
  `board.ParseEvent`; dedupe; per-issue keyed mutex. Build `internal/board/memory` and run the
  resolver against it with zero network. *Demo: a scripted sequence of events drives the full state
  machine on the fake board; against real GitHub, moving a card resolves the phase exactly once.*
- **M2 — Claude Runner (mocked brain).** Wire the `os/exec` call to the `claude` CLI + JSON-envelope
  parsing, but stub the model with a fake binary / canned responses (inject the command path so tests
  don't shell out). *Demo: end-to-end column transitions with fake brainstorm/plan/execute.*
- **M3 — Real brainstorm loop.** Superpowers brainstorm with context builder; questions ↔ answers loop;
  spec written to body. *Demo: a real idea card iterates to an approved spec entirely on the board.*
- **M4 — Worktree + plan + execute.** Worktree via the `CodeForge` port, `write-plan`, `execute-plan`,
  push branch, open PR. *Demo: an approved spec produces a real PR.*
- **M5 — Hardening.** Failure column + error reporting, retries/backoff, cost logging, prompt-injection
  guardrails, permission scoping, structured logging (`go.uber.org/zap`), a `/runs` status endpoint.
- **M6 (optional) — Polish & second provider.** Tune concurrency, a tiny dashboard, configurable column
  names, multi-repo support. Optionally prove the abstraction by writing a second `Board` impl
  (e.g. Linear) — if M0–M5 leaked no provider types into the core, this is purely additive.

---

## 11. Configuration / env

```
BOARD_PROVIDER             # "github" (selects which Board/CodeForge impl to wire in main.go)
GITHUB_APP_ID / GITHUB_PRIVATE_KEY  (or GITHUB_PAT)
GITHUB_WEBHOOK_SECRET
REPO_OWNER / REPO_NAME / PROJECT_NUMBER
BOARD_NAME                 # name used by `wazir provision` when creating the board (point 1; e.g. "Wazir")
ANTHROPIC creds for the claude CLI (subscription login or API key)
CLAUDE_BIN                 # path to the claude binary (injectable for tests)
REPO_CLONE_PATH / WORKTREE_ROOT
BOT_LOGIN                  # to filter self-events
MAX_BRAINSTORM_TURNS       # safety cap on the question loop
COST_BUDGET_USD_PER_DAY    # circuit breaker
```

---

## 12. Risks & gotchas (read before building)

- **Cost / billing (important).** As of June 15, 2026, `claude -p` / Agent SDK / GitHub Actions usage
  draws from a *separate* monthly Agent SDK credit billed at API rates (no rollover), not your normal
  interactive Claude Code limit. A chatty board burns through it. Mitigations: cap brainstorm turns,
  log per-run cost from the JSON envelope, and add a daily budget circuit breaker.
- **No native Go SDK.** You depend on the `claude` binary being installed and on its CLI flags + JSON
  output schema, which can shift between Claude Code versions. Pin a known version, make `CLAUDE_BIN`
  configurable, assert on the envelope schema, and fail loudly on parse errors.
- **Projects v2 is GraphQL-only.** The classic REST "columns/cards" API is deprecated. Use
  `updateProjectV2ItemFieldValue` with `singleSelectOptionId` via `githubv4`. Don't rely on the
  official GitHub MCP server for project writes — its Projects v2 coverage was still incomplete in
  late 2025; the orchestrator should call GraphQL directly.
- **Board-view refresh quirk.** There have been reports that updating a single-select field via the
  API updates the underlying data but occasionally doesn't refresh the board's visual grouping for an
  existing item. Verify on your board early in M0; the field value is authoritative regardless of the
  visual, so drive logic off the value.
- **Headless is single-turn, not a daemon.** Each invocation runs one turn and exits. Your Go worker
  is the supervisor that persists state and re-invokes. Use an isolated `HOME` / `~/.claude` per
  concurrent run so parallel agents don't corrupt each other's session state.
- **Interactive brainstorm in a non-interactive context.** Superpowers brainstorming normally asks and
  waits. The headless contract (§9) makes it dump its questions and stop; the orchestrator handles the
  turn-taking by translating board comments into the transcript.
- **Prompt injection.** Issue/comment text is attacker-controllable if the repo/board is public. Treat
  card content as untrusted: scope `--allowedTools`, run builds in the worktree with least privilege,
  never expose secrets to the model, and require the human approval gate before any code executes.
- **Webhook loops.** The bot's own comments/moves emit events. Filter by `BOT_LOGIN` and the hidden
  marker, and dedupe by delivery id.
- **Permissions.** A headless agent with shell + file access is a real attack surface. Run it in a
  sandbox/container with a scoped permission profile; never `--dangerously-skip-permissions` on a box
  with credentials.

---

## 13. Out of scope (v1)

- Non-GitHub boards (Linear, Jira) — the `Board`/`CodeForge` interfaces (§4.2) make these *addable*,
  but only the GitHub implementation ships in v1. Writing a second provider is M6/post-v1.
- Auto-merging PRs (keep the human as the final merge gate).
- Multi-tenant / team auth beyond a single installer.

---

## 14. Open questions for brainstorming

1. Trigger model: webhooks (recommended) vs. a simple `time.Ticker` poll of the board for a zero-infra v1?
2. Should context persist via `claude -p --resume` sessions, or always reconstruct from the thread?
3. ~~One repo to start, or design the project/repo mapping for multi-repo from day one?~~
   **Decided (§4.1):** one board for v1; data + write paths are project/repo-aware so multi-board is a
   config change later, not a migration. Still open: start with one repo, or install the App on several
   repos under the one board from day one?
4. Run on a personal box / VPS, or shift execution into the Claude Code GitHub Action instead of a
   custom Go runner (trades infra for ephemeral runners, but worktrees/long loops get awkward)?
5. SQLite (`modernc.org/sqlite`) vs. bbolt for the store — relational queries vs. zero-dependency KV?
