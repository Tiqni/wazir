# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

**M0 and M1 are implemented and merged to `main`** (ports + GitHub provisioning + CLI; webhook
receiver + idempotency + queue + in-memory board + orchestrator with the `Brain` port faked by
`CannedBrain`). **M2 is merged to `main`** (the real `claude`-CLI brain + the live brainstorm loop). **M4 is in
progress** on branch `m4-worktree-plan-execute`: the live forge git operations (clone/worktree/push)
and real `claude` plan/execute turns inside a per-card worktree, producing a real PR. See
`docs/superpowers/specs/2026-06-07-wazir-m4-design.md` and `docs/superpowers/plans/2026-06-07-wazir-m4.md`.
**M5 slice 1 (execution isolation) is in progress** on branch `m5-execution-isolation`: per-run
`CLAUDE_CONFIG_DIR` (suppressing global `~/.claude/CLAUDE.md` and other plugins), `--plugin-dir` for
plan/execute turns, long-lived-token auth (`CLAUDE_CODE_OAUTH_TOKEN`), and a repo-aware brainstorm
(cwd = the card's repo clone). See
`docs/superpowers/specs/2026-06-07-wazir-m5-execution-isolation-design.md` and
`docs/superpowers/plans/2026-06-07-wazir-m5-execution-isolation.md`.

Two source-of-truth documents, both worth reading before non-trivial work:
- **`docs/wazir-init-plan.md`** — the original PRD + technical design + phased plan. Section numbers
  throughout this file (§4.2, §9, …) refer to it.
- **`docs/superpowers/specs/2026-06-02-wazir-m0-design.md`** + **`docs/superpowers/plans/2026-06-02-wazir-m0.md`**
  — the spec and task plan that M0 was actually built from (these reflect decisions that *diverge* from
  the init plan: fig/zap/cobra, bbolt, additive-safe reconcile, per-card repo resolution).

When the init plan and the M0 spec disagree, the M0 spec + the code win.

## What Wazir is

A small long-running Go **orchestrator service** that turns a GitHub Projects v2 board into a
human-gated, AI-driven dev loop. A human writes an idea/bug as a card; Wazir drives it through
brainstorm → spec → plan → build → PR, pausing at every gate for explicit human approval. Wazir
invokes the `claude` CLI (with the Superpowers plugin) headlessly as the per-phase "brain" and owns
all deterministic GitHub state changes itself. Module: `github.com/EmadMokhtar/wazir`. Binary: `wazir`
(daemon `wazird` is M1+, not built).

## Commands

Go 1.24+ (the `go` directive is `1.24.0` — the floor is `testing.T.Chdir` in the config tests, not a
dependency; oauth2 was dropped so it no longer forces 1.25). There is
no Makefile or golangci config — `go vet` is the lint.

```sh
go build ./...
go test ./...                                      # unit suite — no network or credentials
go test ./internal/store/...                       # single package
go test ./internal/board/github/ -run TestMerge    # single test (name is a regex)
go vet ./...

# Opt-in integration test against a REAL Projects v2 board (build tag + env, no file needed):
WAZIR_GITHUB_PAT=$(gh auth token) WAZIR_GITHUB_OWNER_TYPE=user \
WAZIR_PROJECT_OWNER=<login> WAZIR_PROJECT_NUMBER=<n> \
go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v

# Run the CLI (cobra):
go run ./cmd/wazir provision     # create board (if absent) + reconcile columns; idempotent
go run ./cmd/wazir bootstrap     # reconcile + cache an existing board; never creates
go run ./cmd/wazir card move    <issue-node-id> <Phase>
go run ./cmd/wazir card comment <issue-node-id> "<text>"
# persistent flags: --config <path>, --log-level debug|info|warn|error, --log-format console|json
```

## Architecture — the load-bearing rules

These constraints are the whole point of the design. Violating them defeats it.

1. **Two ports, provider-agnostic core (§4.2).** The core depends on exactly two Go interfaces:
   - `Board` (`internal/board`) — the kanban surface: cards, columns, comments, provisioning, event parsing.
   - `CodeForge` (`internal/forge`) — the VCS surface: clone, worktree, branch, push, open PR.
   - **Nothing imports a provider package (`internal/board/github`, `internal/forge/github`) except `cmd/wazir`.** The ports speak domain vocabulary (`Card`, `Comment`, `Phase`, `Event`, `ApprovalSignal`) — never provider concepts (node IDs, GraphQL, `projects_v2_item`). Provider-specific mapping (column name ↔ `Phase`, `IsBot`, `Dedup`, the marker) lives *inside* the github impl. This rule is enforced by `internal/orchestrator/imports_test.go`, which fails if the core ever imports a provider.
   - Honest-abstraction goal: an in-memory fake `Board` (`internal/board/memory`, **M1, not built yet**) should run the entire state machine with no network. Until then the abstraction is exercised by the `projectsAPI` fake in the github impl's unit tests (below).

2. **The board is the source of truth.** A card's phase = its `Status` single-select field value.
   The orchestrator re-derives what to do from the board + comment thread on every event. No hidden
   state the user can't see on the board.

3. **The model reasons; the orchestrator acts.** Claude is invoked only to return structured text.
   The orchestrator performs *all* provider I/O (post comment, rewrite spec body, move column, open PR)
   deterministically through the ports. **Never let the model move columns or open PRs directly.**

4. **Human gates are explicit.** A card only advances past a review gate on an explicit human signal
   (a label, an approval comment, or a column move). Silence never auto-advances. Wazir never merges.

5. **Idempotent & serialized (mostly M1).** Webhooks fire repeatedly and out of order: dedupe on the
   delivery id, track `last_processed_comment_id`, serialize work per card with a keyed mutex plus a
   cross-restart TTL lock; different cards run concurrently. The store is **bbolt** (KV), keyed
   project/card-aware; the deliveries/runs/lock buckets and the receiver are M1+. Provisioning itself
   is already idempotent today.

6. **Carry `(project_id, repo, item)` on every store row and every write (§4.1).** Multi-board is out
   of scope for v1, but the seam exists: `store.BoardRecord` is keyed by project id and `store.CardRecord`
   carries repo + issue number. Going multi-board later is a config change, not a migration. Don't build
   the routing yet.

7. **Isolated execution (M4).** Each card's plan/build runs in its own `git worktree` and ideally an
   isolated `HOME`/`~/.claude` per concurrent `claude` run.

## What M0 actually built (the parts that span files)

- **The `projectsAPI` GraphQL seam.** All Projects v2 GraphQL lives behind a narrow interface in
  `internal/board/github/projects.go`. Provisioning orchestration (`EnsureProvisioned` in `board.go`)
  and per-card resolution are unit-tested against a **fake** `projectsAPI` (`provision_test.go`); the
  real `githubv4`-backed implementation (`projects_gql.go`) is exercised **only** by the build-tagged
  integration test against a live board. This keeps GraphQL-heavy code off the fast unit path while
  still validating it for real. The reconcile *merge* itself is a pure function (`reconcile.go`).
- **Additive-safe provisioning.** `provision`/`bootstrap` reconcile the `Status` options via
  `updateProjectV2Field`: existing options are re-sent **with their ids** (preserved — cards aren't
  orphaned), missing §3 columns are appended **without** an id (created). The default `Todo`/`In Progress`/`Done`
  are left in place by default. `--prune` reconciles to *exactly* Wazir's columns (deleting extras,
  canonical order) but refuses to delete a column that still holds cards (`ErrColumnsOccupied`) unless
  `--force`; occupancy comes from the `projectsAPI.StatusOptionItemCounts` seam.
- **Per-card repo resolution.** `resolveCard` (`board.go`): bbolt cache → GraphQL `node()` lookup →
  cache-write, then the repo is checked against the `repos` allow-list (`ErrRepoNotAllowed`). Repo is
  never global config; one board may hold issues from many repos (§4.1). The write paths
  (`PostComment`/`SetBody`/`GetCard`) go through this; `MoveTo` is board-item-scoped and doesn't.
- **Auth seam.** `internal/githubauth.HTTPClient` returns an authenticated `*http.Client` shared by the
  REST + GraphQL clients. PAT ships; GitHub App is scaffolded behind `auth: app` (`ErrAppAuthNotWired`).

## Configuration (fig)

`internal/config` loads with **kkyr/fig**: an *optional* nested `wazir.yaml` (sections `github` /
`project` / `store`, plus top-level `repos`, `bot_login`) with environment overrides named
`WAZIR_<SECTION>_<FIELD>` — e.g. `WAZIR_GITHUB_PAT`, `WAZIR_GITHUB_OWNER_TYPE`, `WAZIR_PROJECT_NUMBER`.
With no file present, config comes from env + struct `default:` tags (via `fig.IgnoreFile()`). Secrets
(PAT, webhook secret) come from env, not the committed file. Entry point: `config.Load(path)`;
`--config` sets the path. `wazir.example.yaml` is the template; `/wazir.yaml` is gitignored.
The `claude` section also carries `plugin_dir` / `setting_sources` (env `WAZIR_CLAUDE_PLUGIN_DIR` /
`WAZIR_CLAUDE_SETTING_SOURCES`); the daemon authenticates `claude` via `CLAUDE_CODE_OAUTH_TOKEN` in
the environment (not the committed file).

## Testing strategy

- **Unit (default `go test ./...`)** — no network or credentials. REST writes use `httptest` with
  go-github's `BaseURL` redirected; the `projectsAPI` and provisioning logic use hand-written fakes;
  `ParseEvent` is table-driven over canned webhook fixtures in `internal/board/github/testdata/`.
- **Integration (`-tags integration`)** — `internal/board/github/integration_test.go` runs real
  provisioning against a live board (env-driven, skips if `WAZIR_PROJECT_NUMBER` is unset). This is the
  permanent guard for the raw `githubv4` code against API/library drift.

## State machine (§3)

Columns = `Status` options: `Inbox` → `Brainstorming` → `Awaiting Answers` (loops back to
`Brainstorming` on human reply) → `Spec Review` → `Planning` → `Building` → `PR Review` → `Done`, with a
`Failed` column any phase can drop into on error. The `Phase` constants are camelCase tokens
(`AwaitingAnswers`, `SpecReview`, `PRReview`); the spaced display names live only in the github impl's
mapping. Owner alternates human/orchestrator per column — see the transition table in §3.

## Claude Runner contract (§8.4, §9) — M2, not built

No official Go SDK — shell out to the `claude` CLI via `os/exec` (`claude -p <prompt> --output-format
json`), `json.Unmarshal` the envelope, then extract the **last fenced ```json block** from the result
text as the phase contract (`brainstorm`/`plan`/`execute` schemas in §9). Each invocation is one phase
turn and exits; the Go worker is the supervisor. Always use `exec.CommandContext` with a timeout,
capture stderr, set `cmd.Dir` to the worktree for plan/execute, and persist `session_id` + cost.
`CLAUDE_BIN` should be injectable so tests stub the binary instead of shelling out.

## Layout

```
cmd/wazir/             # cobra CLI: root.go, logging.go (zap), provision.go, card.go, serve.go, main.go
internal/config/       # fig config: nested wazir.yaml + WAZIR_ env overrides (incl. claude section)
internal/board/        # Board port + domain types (Phase, Card, Comment, Event, …)
internal/board/github/ # GitHub Board impl: mapping, reconcile (pure), projectsAPI seam + githubv4
                       #   impl, board.go (provisioning + writes + GetCard phase + Hydrate), parse_event.go
internal/board/memory/ # in-memory fake Board (M1): runs the full state machine, no network
internal/forge/        # CodeForge port + ErrNotImplemented
internal/forge/github/ # GitHub forge: OpenPR (clone/worktree/push are M4 stubs)
internal/githubauth/   # token-source seam → *http.Client (PAT now, App scaffolded)
internal/store/        # Store interface + bbolt impl + memory impl (tests)
internal/orchestrator/ # provider-free core: Resolver + Worker + Brain port (CannedBrain fake) + transcript
internal/claude/       # Brain impl (M2): Runner (exec + JSON-array envelope) + ClaudeBrain (live brainstorm)
internal/queue/        # per-card serialized dispatch: keyed mutex + cross-restart TTL lock (M1)
internal/server/       # net/http webhook receiver: ParseEvent → dedupe → enqueue (M1)
```

Planned but absent: worktree/plan/execute live path (M4); `runs`/cost persistence + budget breaker (M5).

## Key libraries

- `google/go-github` (REST: issues, comments, PRs, webhook parse) and `shurcooL/githubv4` (typed
  GraphQL — **required** for Projects v2; REST can't touch v2 cards).
- PAT auth is a hand-rolled bearer-token `http.RoundTripper` in `internal/githubauth` (no oauth2
  dependency — it was just setting one header); `bradleyfalzon/ghinstallation` (GitHub App) scaffolded.
- `go.etcd.io/bbolt` (store), `spf13/cobra` (CLI), `go.uber.org/zap` (logging), `kkyr/fig` (config).

> Note: this set supersedes the init plan's original §5 suggestions (`caarlos0/env`, `log/slog`,
> `modernc.org/sqlite`); init-plan §5 has been updated to record the M0 choices.

## Build order (§10)

**M0 ✅** ports + GitHub plumbing + provisioning → **M1 ✅** webhook receiver + idempotency + queue +
in-memory Board → **M2 ✅** Claude Runner (real `claude`-CLI brain + live brainstorm loop; M2+M3
collapsed; plan/execute deferred to M4) → **M4 🚧** worktree + plan + execute + PR (live forge + plan/execute brain; container isolation + budget breaker stay M5) → **M5** hardening →
**M6** (optional) polish + second provider. Each milestone is independently shippable and testable.

## Gotchas (§12)

- **Projects v2 is GraphQL-only** — `updateProjectV2ItemFieldValue` (moves) and `updateProjectV2Field`
  (column options) via `githubv4`; the classic REST columns/cards API is deprecated. Don't rely on the
  GitHub MCP server for project writes.
- **Preserve single-select option ids when reconciling** — `updateProjectV2Field` replaces the whole
  option set; re-send existing options *with* their ids or you orphan every card sitting in them. This
  is the §9 risk, validated live; keep `reconcile.go`'s "existing keep id, new omit id" invariant.
- **Provisioning is reconcile, not create-from-blank** — a new board ships with `Todo`/`In Progress`/`Done`;
  add missing columns, never blindly recreate. `provision` must stay idempotent.
- **Webhook loops** — the bot's own comments/moves emit events. Stamp bot content with the hidden marker
  (`<!-- wazir -->`), filter by `bot_login`, dedupe by delivery id (M1 receiver).
- **Prompt injection (M2+)** — issue/comment text is attacker-controllable. Scope `--allowedTools`
  tightly, run builds least-privilege in the worktree, never expose secrets to the model, never
  `--dangerously-skip-permissions` on a box with credentials.
- **Cost (M2+)** — `claude -p` headless usage draws from a separate metered Agent SDK credit. Cap
  brainstorm turns, log per-run cost, add a daily budget circuit breaker.
- **CLI drift (M2+)** — verify `claude` flags/JSON schema against the installed version; pin it and fail
  loudly on parse errors.
```
