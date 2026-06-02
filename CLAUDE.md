# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Status

Greenfield. The repo currently contains only `README.md`, `LICENSE`, and the authoritative
design doc **`docs/wazir-init-plan.md`** (a full PRD + technical design + phased plan). No Go code
exists yet. **Read `docs/wazir-init-plan.md` before implementing anything** — section numbers below
(§4.2, §9, etc.) refer to it, and it is the source of truth for the design decisions summarized here.

## What Wazir is

A small long-running Go **orchestrator service** that turns a GitHub Projects v2 board into a
human-gated, AI-driven dev loop. A human writes an idea/bug as a card; Wazir drives it through
brainstorm → spec → plan → build → PR, pausing at every gate for explicit human approval. Wazir
invokes the `claude` CLI (with the Superpowers plugin) headlessly as the per-phase "brain" and owns
all deterministic GitHub state changes itself. Module: `github.com/EmadMokhtar/wazir`. Binary: `wazir`
(daemon: `wazird`).

## Commands

Standard Go tooling (Go 1.22+):

```sh
go build ./...
go test ./...
go test ./internal/orchestrator/...           # single package
go test ./internal/orchestrator/ -run TestName # single test
go vet ./...
go install github.com/EmadMokhtar/wazir/cmd/wazir@latest
```

CLI subcommands (to be built): `wazir provision` (create board + reconcile columns), `wazir bootstrap`
(cache IDs for an existing board), `wazird` (run the service).

## Architecture — the load-bearing rules

These constraints are the whole point of the design. Violating them defeats it.

1. **Two ports, provider-agnostic core (§4.2).** The core depends on exactly two Go interfaces:
   - `Board` (`internal/board`) — the kanban surface: cards, columns, comments, provisioning, event parsing.
   - `CodeForge` (`internal/forge`) — the VCS surface: clone, worktree, branch, push, open PR.
   - **`internal/orchestrator` may import `internal/board` and `internal/forge` (the interfaces) but NEVER a provider package** (`internal/board/github`, etc.). Providers are injected in `cmd/wazir/main.go`. The interfaces speak domain vocabulary (`Card`, `Comment`, `Phase`, `Event`, `ApprovalSignal`) — never provider concepts (node IDs, GraphQL, `projects_v2_item`). Provider-specific mapping (e.g. column name ↔ `Phase`, `IsBot`, `Dedup`) lives *inside* the implementation.
   - Honest-abstraction test: `internal/board/memory` is an in-memory fake `Board` that the **entire** state machine runs against with no network. Build it as part of M1 and keep it working.

2. **The board is the source of truth.** A card's phase = its `Status` single-select field value.
   The orchestrator re-derives what to do from the board + comment thread on every event. No hidden
   state the user can't see on the board.

3. **The model reasons; the orchestrator acts.** Claude is invoked only to return structured text.
   The orchestrator performs *all* provider I/O (post comment, rewrite spec body, move column, open PR)
   deterministically through the ports. **Never let the model move columns or open PRs directly.**

4. **Human gates are explicit.** A card only advances past a review gate on an explicit human signal
   (a label, an approval comment, or a column move). Silence never auto-advances. Wazir never merges.

5. **Idempotent & serialized.** Webhooks fire repeatedly and out of order. Dedupe on the delivery id
   (`deliveries` table); track `last_processed_comment_id`. Serialize work per card with a keyed mutex
   (in-process) plus a TTL advisory lock in SQLite (across restarts); different cards run concurrently.

6. **Carry `(project_id, repo, item)` on every store row and every write (§4.1).** Multi-board is out
   of scope for v1, but leave the seam: data and write paths are project/repo-aware now so going
   multi-board later is a config change, not a schema migration. Do NOT build the routing yet.

7. **Isolated execution.** Each card's plan/build runs in its own `git worktree` (so cards progress in
   parallel without colliding) and ideally an isolated `HOME`/`~/.claude` per concurrent `claude` run.

## State machine (§3)

Columns = `Status` options: `Inbox` → `Brainstorming` → `Awaiting Answers` (loops back to
`Brainstorming` on human reply) → `Spec Review` → `Planning` → `Building` → `PR Review` → `Done`, with
a `Blocked`/`Failed` column any phase can drop into on error. Owner alternates human/orchestrator per
column — see the transition table in §3.

## Claude Runner contract (§8.4, §9)

No official Go SDK — shell out to the `claude` CLI via `os/exec` (`claude -p <prompt> --output-format
json`), `json.Unmarshal` the envelope, then extract the **last fenced ```json block** from the result
text as the phase contract (`brainstorm`/`plan`/`execute` schemas in §9). Each invocation is **one
phase turn** and exits; the Go worker is the supervisor that strings turns together. Always use
`exec.CommandContext` with a timeout, capture stderr, set `cmd.Dir` to the worktree for plan/execute,
and persist `session_id` + cost to the `runs` table. `CLAUDE_BIN` is injectable so tests stub the
binary instead of shelling out.

## Suggested layout (§5.1)

```
cmd/wazir/             # main.go (wire deps, http server + worker pool), provision.go
internal/config/       # env config
internal/board/        # Board port + domain types; github/ (impl), memory/ (test fake)
internal/forge/        # CodeForge port; github/ (impl)
internal/claude/       # os/exec the claude CLI; parse JSON envelope + phase contract
internal/orchestrator/ # state resolver, context builder, phase dispatch — ports only, no providers
internal/store/        # sqlite (modernc.org/sqlite, pure Go) or bbolt
internal/queue/        # per-issue serialized goroutine pool
```

## Key libraries (§5)

`google/go-github` (REST: issues, comments, PRs, webhook parse), `shurcooL/githubv4` (typed GraphQL —
**required** for Projects v2: provisioning + column moves; REST can't touch v2 cards),
`bradleyfalzon/ghinstallation` (GitHub App tokens) or `golang.org/x/oauth2` (PAT),
`modernc.org/sqlite` (pure-Go, no cgo) or `go.etcd.io/bbolt`, `net/http` (+ optional `go-chi/chi`),
`caarlos0/env`/`kelseyhightower/envconfig` (config), `log/slog` (logging).

## Build order (§10)

M0 ports + GitHub plumbing + provisioning → M1 webhook receiver + idempotency + queue + in-memory
Board → M2 Claude Runner (mocked brain) → M3 real brainstorm loop → M4 worktree + plan + execute + PR
→ M5 hardening → M6 (optional) polish + second provider. Each milestone is independently shippable and
testable.

## Gotchas (§12)

- **Projects v2 is GraphQL-only** — use `updateProjectV2ItemFieldValue` with `singleSelectOptionId` via
  `githubv4`. The classic REST columns/cards API is deprecated; don't rely on the GitHub MCP server for
  project writes.
- **Provisioning is reconcile, not create-from-blank** — a new v2 board ships with a default `Status`
  field (`Todo`/`In Progress`/`Done`). Read existing options, add missing columns, decide a policy for
  defaults. Never blindly recreate (→ duplicates). `provision` must be idempotent.
- **Webhook loops** — the bot's own comments/moves emit events. Stamp bot content with a hidden marker
  (e.g. `<!-- orchestrator -->`), filter by `BOT_LOGIN`, and dedupe by delivery id.
- **Prompt injection** — issue/comment text is attacker-controllable. Scope `--allowedTools` tightly,
  run builds least-privilege in the worktree, never expose secrets to the model, never
  `--dangerously-skip-permissions` on a box with credentials.
- **Cost** — `claude -p` headless usage draws from a separate metered Agent SDK credit. Cap brainstorm
  turns (`MAX_BRAINSTORM_TURNS`), log per-run cost, add a daily budget circuit breaker.
- **CLI drift** — verify `claude` flags/JSON schema against the installed version; pin a known version
  and fail loudly on parse errors.
```