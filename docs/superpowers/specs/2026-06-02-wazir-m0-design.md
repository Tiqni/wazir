# Wazir M0 — Ports + GitHub Plumbing + Provisioning (Design Spec)

**Date:** 2026-06-02
**Status:** Approved for planning
**Scope:** Milestone M0 only (see `docs/wazir-init-plan.md` §10). M1–M6 are out of scope here.
**Source of truth:** `docs/wazir-init-plan.md` — section references below (§4.2, §6, §7, etc.) point at it.

This spec turns the M0 milestone into a buildable, testable slice. It records the decisions made
during brainstorming and the design that follows from them. It does **not** restate the whole
architecture — read the init plan for that.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| First slice | **M0 literally** | Ports + real GitHub impl + provisioning + `provision`/`bootstrap`. |
| GitHub auth | **PAT-first, App seam scaffolded** | Token-source abstraction ships PAT now; GitHub App is a config flip later. |
| Board owner | **`OWNER_TYPE` config (user \| org)** | Owner-id lookup branches; reconcile logic is shared. |
| Store | **bbolt** (`go.etcd.io/bbolt`) | Zero-dependency KV. Later tables (deliveries/runs/locks) hand-rolled in M1+. |
| Reconcile policy | **Additive-safe; `--prune` deferred** | Never delete a Status option in M0. |
| Repo model | **Per-card repo resolution** | A board may mix repos; repo is derived from each card, not global config. |
| Test strategy | **httptest unit tests (primary) + opt-in integration test (`//go:build integration`)** | No record/replay. |
| CLI framework | **stdlib `flag` + manual subcommand dispatch** | Keep dependencies light. |

---

## 2. Deliverable & demo

M0 ships a `wazir` binary that can stand up and cache a board and exercise the `Board` write surface
from the CLI.

Subcommands:

- `wazir provision` — create the board if absent, reconcile the §3 columns (additive-safe), cache IDs.
  **Idempotent**: running twice converges with no duplicate fields or options.
- `wazir bootstrap` — read + cache IDs for a board created by hand (the read half of `provision`;
  shares the reconcile-and-cache code path, minus creation).
- `wazir card move <issue> <phase>` — dev/demo command: move a card via the `Board` port.
- `wazir card comment <issue> <text>` — dev/demo command: post a comment via the `Board` port.

`<issue>` accepts an issue **node id** or a `owner/name#number` reference; the impl resolves it to the
project item id and repo (see §6).

**Demo (acceptance):**
1. `wazir provision` builds the board with the nine §3 columns from scratch.
2. Run `wazir provision` again → no duplicate options, same cached IDs.
3. `wazir card move <issue> Brainstorming` moves the card; `wazir card comment <issue> "hello"`
   posts a marker-stamped comment — both through the `Board` interface, no provider types in the caller.

---

## 3. Package layout (M0 subset of init-plan §5.1)

```
cmd/wazir/main.go          # subcommand dispatch (stdlib flag), wire deps from config
cmd/wazir/provision.go     # provision + bootstrap commands
cmd/wazir/card.go          # dev move/comment commands
internal/config/           # env config (caarlos0/env)
internal/board/            # Board PORT + domain types (Card, Comment, Phase, Event, ApprovalSignal, BoardSpec)
internal/board/github/     # GitHub impl: provisioning, MoveTo, PostComment, SetBody, ParseEvent, resolution
internal/forge/            # CodeForge PORT (full interface defined)
internal/forge/github/     # GitHub impl: OpenPR (Clone/worktree/push deferred to M4)
internal/githubauth/       # token-source seam: PAT now, App scaffolded -> *http.Client
internal/store/            # Store interface + bbolt impl + memory impl (for tests)
```

**Dependency rule (load-bearing, init-plan §4.2/§5.1):** nothing under `internal/board/github` or
`internal/forge/github` is imported by anything except `cmd/wazir/main.go`. The ports speak domain
vocabulary only (`Card`, `Comment`, `Phase`, `Event`) — never provider concepts (node ids, GraphQL,
`projects_v2_item`). A documented test (or import-lint) guards against leaks.

The full `Board` and `CodeForge` interfaces from §4.2 are **defined** now so the shape is locked, even
where M0 only implements a subset.

---

## 4. Domain types & the Board port

Defined in `internal/board` exactly as init-plan §4.2. Key points:

- `Phase` constants: `Inbox`, `Brainstorming`, `AwaitingAnswers`, `SpecReview`, `Planning`, `Building`,
  `PRReview`, `Done`, `Failed`.
- `Card` carries `Repo string // "owner/name"` — the spine of multi-repo support (§6).
- `Board` interface methods: `EnsureProvisioned`, `GetCard`, `ListCards`, `PostComment`, `SetBody`,
  `MoveTo`, `ParseEvent`.
- `Event`, `EventKind`, `ApprovalSignal`, `BoardSpec` as specified.

**Phase ↔ column display-name mapping lives *inside* `internal/board/github`** (not in the core).
Canonical display names:

| Phase | Column name |
|---|---|
| `Inbox` | `Inbox` |
| `Brainstorming` | `Brainstorming` |
| `AwaitingAnswers` | `Awaiting Answers` |
| `SpecReview` | `Spec Review` |
| `Planning` | `Planning` |
| `Building` | `Building` |
| `PRReview` | `PR Review` |
| `Done` | `Done` |
| `Failed` | `Failed` |

§3's "Blocked/Failed" is realized as a single **`Failed`** column (1:1 with the `Phase` enum).
Configurable column names are an M6 concern.

---

## 5. GitHub `Board` implementation

`internal/board/github`. The orchestrator calls the interface; everything here is hidden behind it.

**Caches** (in bbolt + in-memory), per §7:
- Project node id, Status field id, `map[Phase]optionID`, and the reverse `map[optionID]Phase`
  (the reverse is needed by `ParseEvent` and `GetCard` to translate a column back to a `Phase`).

**`EnsureProvisioned(ctx, BoardSpec)`** — init-plan §6.1:
1. Resolve the owner node id (`user(login:)` vs `organization(login:)` per `OWNER_TYPE`).
2. Find the project by `PROJECT_NUMBER`. In `provision` mode, if absent, `createProjectV2`. In
   `bootstrap` mode, do not create — error if absent.
3. Read the existing `Status` single-select field and its options.
4. **Additive-safe reconcile:** merge the missing §3 columns into the existing option set,
   **preserving existing option ids** (reuse `Done` by name-match; leave `Todo`/`In Progress`
   untouched). Never delete. See §9 (risk) for the mutation mechanics.
5. Persist project id + field id + per-phase option ids to bbolt (the cache the rest of the system
   reads). Idempotency: a second run is a no-op convergence.

**`bootstrap`** = steps 1, 3, 4(read-only check), 5 — read and cache, no creation.

**`MoveTo(ctx, cardID, phase)`** — resolve `cardID` → `project_item_id` (§6 cache), translate
`phase` → cached `singleSelectOptionId`, then `updateProjectV2ItemFieldValue` (init-plan §8.5).

**`PostComment(ctx, cardID, body)`** — resolve `cardID` → `(repo, issue_number)` (§6), then REST
`IssueComments.Create`. Body is stamped with a hidden marker `<!-- wazir -->` so `ParseEvent` can flag
the bot's own events.

**`SetBody(ctx, cardID, markdown)`** — resolve to `(repo, issue_number)`, REST `Issues.Edit`. The
original idea is preserved in a collapsed `<details>` block at the top of the new body (init-plan §8.5).

**`ParseEvent(headers, payload)`** — `github.ValidatePayload` (uses `GITHUB_WEBHOOK_SECRET`) →
`github.ParseWebHook` → type-switch (`*IssuesEvent`, `*IssueCommentEvent`, `*ProjectV2ItemEvent`) →
domain `Event`. Sets `IsBot` (author == `BOT_LOGIN` **or** body contains the marker) and `Dedup`
(`X-GitHub-Delivery` header). Drops events whose project node id ≠ the configured project, and events
whose card repo is not in the allow-list (§6). **This is M0's best pure-logic TDD target** — canned
webhook JSON → expected `Event` — even though the receiver that *calls* it lands in M1.

---

## 6. Multi-repo: per-card repo resolution

A Projects v2 board is owned at the user/org level and may hold Issues from **many repos** at once
(init-plan §4.1). Repo is therefore a **per-card property derived from the card's content**, never a
global config value.

**Resolution machinery** (`internal/board/github` + `internal/store`):

- **bbolt bucket `cards`**, keyed by `issue_node_id`, value JSON
  `{repo, issue_number, project_item_id}`. This is the §7 `issues` row in KV form.
- The cache is populated whenever we read a card (`GetCard`/`ListCards`) or parse an event
  (`ParseEvent`).
- **Cold-cache fallback** — a GraphQL `node(id:)` lookup:
  ```graphql
  node(id: $issueNodeId) {
    ... on Issue { number repository { nameWithOwner } }
  }
  ```
  so a write resolves correctly even before the cache is warm.
- Note `project_item_id` (the card in the project) is a **different** id from `issue_node_id` (the
  Issue). `MoveTo` needs the former; REST writes need `repo` + `issue_number`. Both ride on this one
  cache, so multi-repo support and the issue↔item mapping are the same mechanism.

**`Card.Repo`** is the spine: `GetCard` populates it from `repository { nameWithOwner }`; `OpenPR`
consumes it; `PostComment`/`SetBody` resolve through the cache. The `Board`/`CodeForge` interfaces and
the state machine are **unchanged** by multi-repo — the tell that the abstraction holds.

**Repo allow-list (`REPOS` config):** Wazir refuses to act on a card whose repo is not allow-listed.
Primary value is in M1's webhook receiver and prompt-injection containment, but the seam is defined in
M0 and enforced by `ParseEvent` and the dev `card` commands.

**Auth must cover every board repo** (init-plan §4.1): a fine-grained PAT must be granted access to
all allow-listed repos; a GitHub App must be installed on each. Documented as a setup prerequisite.

**Draft cards:** a v2 card can be a draft issue (board-only, no backing repo Issue) — those have no
repo and can't take REST comments or host a PR. M0 treats cards as repo-backed Issues and skips/flags
drafts. Convert-or-skip handling is an M1+ edge case.

---

## 7. GitHub `CodeForge` + auth seam

- The **full** `CodeForge` interface is defined (`Clone`, `CreateWorktree`, `RemoveWorktree`,
  `PushBranch`, `OpenPR`). M0 implements **`OpenPR`** only (`PullRequests.Create`), unit-tested via
  httptest. The remaining methods return a clear `errNotImplemented` sentinel (delivered in M4).
- **`internal/githubauth`** exposes one function returning an authenticated `*http.Client` shared by
  go-github (REST) and shurcooL/githubv4 (GraphQL):
  - PAT path: `oauth2.StaticTokenSource` over `GITHUB_PAT`.
  - App path: `bradleyfalzon/ghinstallation` transport — **scaffolded behind `GITHUB_AUTH=app` but not
    wired/tested in M0**.
  - The Board/Forge impls receive a ready client; they never see auth details.

---

## 8. Store, config, errors, logging

**Store (`internal/store`):** a `Store` interface with a **bbolt** impl and a **memory** impl (for
tests). Buckets:
- `boards` — keyed by `project_id` (carry it now per §4.1, even with one board) → JSON
  `{projectNumber, projectNodeID, statusFieldID, options{phase→optionID}, owner, ownerType}`.
- `cards` — keyed by `issue_node_id` → JSON `{repo, issue_number, project_item_id}` (§6).

bbolt file path from config.

**Config (`internal/config`, via caarlos0/env) — M0 subset:**
```
GITHUB_AUTH            # pat | app   (selects token source)
GITHUB_PAT             # when GITHUB_AUTH=pat
GITHUB_APP_ID / GITHUB_PRIVATE_KEY / GITHUB_APP_INSTALLATION_ID   # scaffolded (app)
OWNER_TYPE             # user | org
PROJECT_OWNER          # login of the user/org that owns the board
PROJECT_NUMBER         # the Projects v2 board number
BOARD_NAME             # name used by `wazir provision` when creating the board
REPOS                  # comma-separated allow-list "ownerA/x,ownerB/y"
BOT_LOGIN              # to filter self-events
GITHUB_WEBHOOK_SECRET  # required by ParseEvent payload validation
WAZIR_DB               # bbolt file path
```
> Note: single-repo `REPO_OWNER`/`REPO_NAME` from the init-plan §11 list are intentionally **dropped**
> in favour of per-card resolution + the `REPOS` allow-list (§6).

**Errors:** wrapped with `%w`; sentinels `ErrNotProvisioned`, `ErrUnknownPhase`, `errNotImplemented`.
GraphQL/REST failures carry the operation name for diagnostics.

**Logging:** `log/slog` throughout.

---

## 9. Key risk — de-risk first

**Single-select option mutations are the known soft spot.** The GraphQL mutation that manages a
Status field's options is a *replace-the-set* operation: "additive" means **read existing options,
merge in the missing ones preserving existing option ids, then send the combined set**. Sending bare
names risks recreating options (new ids) and orphaning any cards sitting in them. The exact mutation
shape has also shifted across GitHub API versions.

**First implementation step is a verification spike:** confirm the current `shurcooL/githubv4`
mutation for managing single-select options against a real scratch board (cross-check current GitHub
GraphQL docs), then build the reconcile logic to match. The opt-in integration test (§10) guards this
permanently against API drift.

---

## 10. Testing strategy

- **(A) Primary — httptest unit tests.** Stand up a local fake GitHub server and point go-github and
  githubv4 base URLs at it; assert the requests Wazir sends and feed canned responses. Covers
  provisioning/reconcile-merge logic, `MoveTo`, `PostComment`, `SetBody`, `OpenPR`, and the
  card-resolution fallback. Fast, deterministic, no credentials, CI-friendly.
- **`ParseEvent`** is tested table-driven with canned webhook JSON fixtures — no server needed.
- **(C) Opt-in integration test** (`//go:build integration`, env-gated) runs real provisioning against
  a scratch board to validate the actual GraphQL mutations against current GitHub and guard the §9
  risk. Not part of the default `go test ./...` run.
- The `Store` memory impl backs unit tests that need persistence without bbolt on disk.

---

## 11. Out of M0 scope (M1+)

Webhook HTTP receiver and signature handling at the transport layer, per-issue queue/keyed mutex,
`deliveries`/`runs`/`locks` usage, the Claude runner, worktree/clone/push (`CodeForge` methods other
than `OpenPR`), state resolver, context builder, the `wazird` daemon loop, fully-wired+tested GitHub
App auth, `--prune`, draft-card conversion, and configurable column names.

---

## 12. Acceptance checklist

- [ ] `Board` and `CodeForge` interfaces + domain types defined in `internal/board` / `internal/forge`.
- [ ] Dependency rule enforced (no provider import outside `main.go`).
- [ ] `wazir provision` creates the board with the nine §3 columns from scratch, idempotently.
- [ ] Additive-safe reconcile preserves existing option ids; reuses `Done`; leaves defaults.
- [ ] `wazir bootstrap` caches IDs for a hand-made board without creating anything.
- [ ] `MoveTo`, `PostComment` (marker-stamped), `SetBody` (`<details>` history) work through the port.
- [ ] Per-card repo resolution works for a board mixing ≥2 repos (cache + `node(id:)` fallback).
- [ ] `REPOS` allow-list rejects out-of-scope cards.
- [ ] `ParseEvent` maps canned `issues` / `issue_comment` / `projects_v2_item` payloads to `Event`,
      setting `IsBot` and `Dedup`.
- [ ] `OpenPR` constructs the correct `PullRequests.Create` request (httptest).
- [ ] PAT auth path works end-to-end; App path compiles behind `GITHUB_AUTH=app`.
- [ ] httptest unit suite green under `go test ./...`; integration test green under `-tags integration`.
