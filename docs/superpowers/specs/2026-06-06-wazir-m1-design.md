# Wazir M1 — Webhook Receiver + Idempotency + Queue + In-Memory Board (Design Spec)

**Date:** 2026-06-06
**Status:** Approved for planning
**Scope:** Milestone M1 only (see `docs/wazir-init-plan.md` §10). M2–M6 are out of scope here.
**Source of truth:** `docs/wazir-init-plan.md` (section refs below — §3, §4.2, §8.x, §9 — point at it) and the
M0 spec `docs/superpowers/specs/2026-06-02-wazir-m0-design.md`. Where the init plan and the shipped M0
code disagree, the code wins.

This spec turns the M1 milestone into a buildable, testable slice. It records the decisions made during
brainstorming and the design that follows. It does **not** restate the architecture — read the init plan
and the M0 spec for that.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| Worker scope | **Full loop with a faked `Brain` port** | Runs the entire state machine on the memory board; M2 swaps in the real `claude` CLI. This is the §4.2 honest-abstraction test. |
| Daemon entry | **`wazir serve` subcommand** | One binary; reuses the existing cobra tree + `--config`/`--log-level`/`--log-format` + `openBoard()` wiring. No second `wazird` main package in v1. |
| Idempotency / locking | **Full** | `deliveries` dedupe + `last_processed_comment_id` + in-process keyed mutex + cross-restart TTL lock (init-plan §8.7; CLAUDE.md lists all three as M1). |
| Real-GitHub wiring | **Deferred** | M1 proves the machine on `internal/board/memory` with zero network. Live GitHub `GetCard` phase-from-Status resolution and label/approval parsing slip to M2/M5. |
| Orchestrator shape | **Pure `Resolver` (decide) + `Worker` (act) split** | Decision logic is I/O-free and exhaustively table-testable; the clearest expression of "the model reasons; the orchestrator acts." Rejected: one combined `Processor` (entangles decision with I/O, harder to test). |

---

## 2. Deliverable & demo

M1 ships a `wazir serve` daemon entrypoint and a provider-agnostic orchestrator core that drives the §3
state machine end-to-end. The **brain** (Claude) and the **forge** worktree/PR side are faked; everything
else is real.

**Demo (acceptance):**

1. **Scripted state machine on the memory board (no network).** A test seeds a card in `Inbox` and
   feeds a sequence of events; the orchestrator drives it through the full loop:
   `Inbox →(pick up)→ Brainstorming →(needs_answers)→ Awaiting Answers →(human reply)→ Brainstorming
   →(spec_ready)→ Spec Review →(approve via column move)→ Planning →(plan_ready)→ Building
   →(complete)→ PR Review`. Asserts phases, posted comments, the rewritten spec body, and that
   **re-delivering any event is a no-op** (idempotency).
2. **Receiver dedupe + per-card serialization.** Replayed webhook deliveries resolve a card exactly
   once; two different cards process concurrently while turns on the same card serialize.
3. `wazir serve --addr :8080` boots: HTTP receiver → dedupe → per-card queue → worker (faked brain),
   wired over the configured `Board`. (Live GitHub end-to-end is M2 — see §10.)

---

## 3. Package layout (delta on M0)

```
✚ internal/board/memory/     # full board.Board fake: runs the state machine, zero network
✚ internal/orchestrator/     # resolver.go, worker.go, brain.go (port), decision.go, transcript.go, phases.go
✚ internal/queue/            # per-card serialized dispatch: keyed mutex + cross-restart TTL lock
✚ internal/server/           # net/http webhook receiver: ParseEvent → dedupe → enqueue
✎ internal/store/            # + deliveries bucket, + locks bucket, + CardRecord.LastProcessedCommentID
✎ cmd/wazir/serve.go         # `wazir serve` assembles server → queue → worker(board, fakeBrain, forge)
```

**Dependency rule (load-bearing, init-plan §4.2/§5.1):** `internal/orchestrator` imports **only**
`internal/board`, `internal/forge`, and its own `Brain` port — never a provider package. The existing
`internal/orchestrator/imports_test.go` guard continues to enforce this. The faked brain (M1) and the
real `internal/claude` brain (M2) are injected at `serve` wiring time, never imported by the core.

---

## 4. The orchestrator core (`internal/orchestrator`)

The heart of M1. Three collaborators behind the two ports.

### 4.1 `Resolver` — pure decision function

```go
// No I/O. Exhaustively table-tested.
func (r *Resolver) Resolve(card board.Card, ev board.Event, lastCommentID string) Decision
```

It maps `(phase, event, who-acted, what's-new-since-lastCommentID)` to exactly one `Decision`. The
table (realizing §3's transitions):

| Phase (card) | Trigger | Decision |
|---|---|---|
| `Inbox` | `CardCreated` or moved to `Brainstorming` | `ActPickUp` (→ Brainstorming, then brainstorm) |
| `Brainstorming` | picked up / re-entered | `ActBrainstorm` |
| `AwaitingAnswers` | human `CommentAdded` (new) | `ActBrainstorm` (move back to Brainstorming, re-run) |
| `SpecReview` | human `CommentAdded` (new, no approval) | `ActBrainstorm` (revision; stay in SpecReview, update body) |
| `SpecReview` | moved to `Planning` (approval) | `ActPlan` |
| `Planning` | entered | `ActPlan` |
| `Building` | entered | `ActExecute` |
| `PRReview` / `Done` | anything | `ActNone` |
| any | bot-authored event, or comment ≤ `lastCommentID` | `ActNone` |

**Approval signal (M1):** a human **column move to `Planning`** (`EventPhaseChanged`, NewPhase=Planning)
is the approval. Label-based approval (`spec-approved`) parsing in the GitHub `ParseEvent` is deferred
(§10) — the memory board emits the move directly in tests.

`Decision` (`decision.go`) is a small struct: `{Action, TargetPhase}` where `Action ∈ {None, PickUp,
Brainstorm, Plan, Execute}`. The brain-result → board-write mapping is **not** in the Decision — it is
deterministic and lives in the Worker (§4.2).

### 4.2 `Worker` — executes a `Decision` via the ports

```go
func (w *Worker) Process(ctx context.Context, ev board.Event) error
```

Flow (per event, already under the per-card lock held by the queue):

1. `card := board.GetCard(ev.CardID)` — fresh phase + full thread (never trust stale event data; §8.2).
2. `lastID := store.GetCard(ev.CardID).LastProcessedCommentID`.
3. `d := resolver.Resolve(card, ev, lastID)`.
4. Execute `d`:
   - `ActPickUp` → `board.MoveTo(Brainstorming)`, then fall through to brainstorm.
   - `ActBrainstorm` → `brain.Brainstorm(ctx, transcript(card))`; then **deterministically**:
     - `needs_answers` → `board.PostComment(questions)` + `board.MoveTo(AwaitingAnswers)`.
     - `spec_ready` → `board.SetBody(spec_markdown)` + `board.MoveTo(SpecReview)`.
   - `ActPlan` → `board.MoveTo(Planning)` (if not there) → `brain.Plan(...)`:
     - `plan_ready` → `board.MoveTo(Building)`, then fall through to execute.
     - `failed` → fail path.
   - `ActExecute` → `brain.Execute(...)`:
     - `complete` → `forge.OpenPR(...)` → `board.PostComment(prURL)` → `board.MoveTo(PRReview)`.
     - `failed` → fail path.
   - `ActNone` → return nil.
5. **Fail path** (any port/brain error or a `failed` phase result): `board.MoveTo(Failed)` +
   `board.PostComment(error report)`. Errors are wrapped with `%w` and logged (zap).
6. On success, advance `store.PutCard(... LastProcessedCommentID = newest human comment id)`.

The Worker owns the deterministic mapping precisely because it is **not** a model judgment — the model
returns a structured result; turning that result into column moves / comments is orchestrator work
(principle: "the model reasons; the orchestrator acts").

**"Fall through" is intentional:** consecutive *orchestrator-owned* phases have no human gate between
them (§3: Planning → Building → PR Review are all orchestrator-driven), so one `Process` turn may chain
`pick-up → brainstorm` or `plan → build → execute → open-PR`. Turns only ever **stop** at a human gate
(`Awaiting Answers`, `Spec Review`, `PR Review`) or on failure — never auto-advancing past one (§2
principle: silence never advances a card past a review gate).

> **M1 forge note:** `forge.OpenPR` exists (M0) but `CreateWorktree`/`PushBranch` are M4 stubs returning
> `ErrNotImplemented`. So the `Building → PR Review` transition runs **for real only against a fake
> forge** (in the scripted test). A live `serve` would hit the M4 stub and drop the card to `Failed` —
> acceptable and honest for M1, since live wiring is deferred (§10). The Worker treats `ErrNotImplemented`
> like any other error (→ `Failed` + comment), not a special case.

### 4.3 `Brain` port + phase contracts

Defined in `internal/orchestrator` (so the core owns the interface), faked in M1, implemented by
`internal/claude` in M2:

```go
type Brain interface {
    Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error)
    Plan(ctx context.Context, in PlanInput) (PlanResult, error)
    Execute(ctx context.Context, in ExecuteInput) (ExecuteResult, error)
}
```

Result types (`phases.go`) mirror the §9 JSON schemas:

```go
type BrainstormResult struct { Status BrainstormStatus; Questions []string; SpecMarkdown string }
                                // Status ∈ {NeedsAnswers, SpecReady}
type PlanResult       struct { Status PhaseStatus; PlanPath, Summary, Error string }
type ExecuteResult    struct { Status PhaseStatus; Branch string; Commits []string; TestSummary, Notes, Error string }
```

`transcript.go` builds the model input: `card.Title` + `card.Body` + the comment thread, each entry
tagged `HUMAN:` or `SYSTEM:` and **excluding** bot-marker content (loop prevention; §8.3). Inputs carry
the transcript + the current phase instruction. The M1 fake (`brainFake` in tests, or a tiny
`internal/orchestrator` test helper) returns canned, scriptable results — no parsing, no exec.

---

## 5. Queue & locking (`internal/queue`)

Serializes turns **per card** while running **different cards concurrently** (init-plan §8.7).

- **In-process:** a keyed mutex — `map[cardID]*sync.Mutex` guarded by an outer `sync.Mutex` — so a card's
  turns never overlap, but distinct cards proceed in parallel goroutines from a bounded worker pool.
- **Cross-restart:** a bbolt `locks` bucket (§7) holding `{owner, expires_at}` per card. `Process`
  acquires the advisory lock (TTL) before acting and releases it with `defer`; a crashed worker's lock
  self-heals once the TTL passes. The owner token is the process/run id.
- **Shutdown:** `serve` cancels the context and drains in-flight work before exit.

The queue is provider-agnostic: it takes a `handler func(ctx, board.Event) error` (the Worker's
`Process`) and a `store.Store` for the lock. It never imports `board/github`.

---

## 6. Webhook receiver + `serve` (`internal/server`, `cmd/wazir/serve.go`)

### 6.1 Receiver (`internal/server`)

A `net/http` handler, provider-agnostic per init-plan §8.1:

1. Read raw headers + body; hand them to `board.ParseEvent` (the GitHub impl does signature
   verification with the webhook secret, type-switch, `IsBot`, and `Dedup` — M0 code).
2. `EventIgnore` → `200 OK`, drop.
3. Bot-authored event (`Comment.IsBot`) → `200 OK`, drop (loop prevention).
4. `store.SeenDelivery(ev.Dedup)` → `200 OK`, drop (idempotency); else `store.MarkDelivery`.
5. `queue.Enqueue(ev)` keyed by `ev.CardID`; respond `200 OK`.

Always returns `200` for accepted-or-dropped events so GitHub does not retry-storm; only a malformed
request or signature failure returns non-2xx. The handler depends only on `board.Board`, `queue`, and
`store` — no provider types.

### 6.2 `wazir serve`

`serve.go` adds a `serve` subcommand to the cobra tree (`--addr`, default `:8080`). It calls
`openBoard()` (existing M0 wiring), constructs the store-backed queue, the worker with the **faked
brain** + the GitHub forge, mounts the receiver, and runs the HTTP server until `SIGINT`/`SIGTERM`,
then drains the queue. M2 changes one line here: inject the real `internal/claude` brain instead of the
fake.

---

## 7. Store extensions (`internal/store`)

Additive — existing `boards`/`cards` buckets and tests are unchanged.

- **`deliveries` bucket** — webhook idempotency (init-plan §7):
  - `SeenDelivery(id string) (bool, error)`
  - `MarkDelivery(id string) error`
- **`locks` bucket** — cross-restart advisory lock with TTL (§5):
  - `AcquireLock(cardID, owner string, ttl time.Duration) (acquired bool, err error)`
  - `ReleaseLock(cardID, owner string) error`
  - Value JSON `{owner, expires_at}`; acquire succeeds if absent, owned by `owner`, or expired.
- **`CardRecord` gains `LastProcessedCommentID string`** — re-delivered comment events don't re-trigger
  a turn (§8.7). Carried through the existing `GetCard`/`PutCard` merge paths.

Both impls updated: `bbolt.go` (real) and `memory.go` (tests). `runs`/cost logging stays **out** — M2/M5.

---

## 8. Memory `Board` (`internal/board/memory`)

A full `board.Board` implementation backed by maps + a `sync.Mutex`, so the entire state machine runs
with zero network (the §4.2 honest-abstraction test, promised for M1):

- `EnsureProvisioned` — no-op/records the spec.
- `GetCard` / `ListCards` — return seeded cards with their current `Phase` and thread.
- `PostComment` — appends a bot-marked (`IsBot=true`) comment.
- `SetBody` — replaces the card body. The GitHub impl's `<details>` "original idea" history is **not**
  required here; keep it simple and just store the new body.
- `MoveTo` — sets the card's `Phase`.
- `ParseEvent` — decodes a simple internal JSON envelope into a `board.Event` (so the receiver path can
  be exercised against the memory board too).
- **Test helpers:** `Seed(card)` and `Emit(event)` to script sequences.

This package lives under `internal/board/memory` exactly as the init-plan §5.1 layout anticipates.

---

## 9. Testing strategy

- **Centerpiece — full state machine (memory board, fake brain, fake forge).** The scripted sequence in
  §2 demo (1), asserting every phase transition, the posted questions/PR-link comments, the rewritten
  spec body, and idempotency (re-delivering an event changes nothing).
- **`Resolver`** — table-driven over **every** `(phase, event)` pair, including bot events and
  already-processed comments → `ActNone`. Pure, instant.
- **`Worker`** — each `Decision` branch with a fake brain/forge: `needs_answers` vs `spec_ready`
  mapping, the approval→plan→build→PR chain, and the fail path (brain error and `ErrNotImplemented`
  forge both → `Failed` + error comment).
- **`queue`** — same-card turns serialize; different cards run concurrently; TTL lock blocks a second
  acquirer and self-heals after expiry.
- **`server`** — `httptest`: dedupe drops a replayed delivery; bot/ignore events drop; a good event
  enqueues exactly once. (Signature verification is already covered by M0's `ParseEvent` tests.)
- **`store`** — `deliveries` and `locks` buckets round-trip and the lock TTL math is correct, in both
  bbolt and memory impls.

All green under `go test ./...` with no network or credentials. No new integration test is required for
M1 (live GitHub is deferred); the M0 integration test remains the live guard.

---

## 10. Out of M1 scope (M2+)

Deferred as deliberate seams, not gaps:

- **GitHub `GetCard` phase-from-Status resolution** and **label-based approval parsing** in
  `ParseEvent` — needed for a fully-live `serve`; M2/M5.
- **Real `claude` brain** (`internal/claude`, the `os/exec` runner + JSON-envelope + §9 contract
  parsing) — M2; injected in place of the fake at `serve` wiring.
- **Worktree/clone/push + live PR** (`CodeForge` methods beyond `OpenPR`) — M4. The live
  `Building → PR Review` transition only fully works against a fake forge until then.
- **`runs` bucket / cost logging, retries + backoff, daily budget circuit breaker, `/runs` status
  endpoint, prompt-injection guardrails, permission scoping** — M5.
- **Time-ticker polling fallback** (init-plan open question #1) — webhooks only in M1.

---

## 11. Acceptance checklist

- [ ] `internal/board/memory` implements the full `board.Board` port; the scripted state machine runs
      end-to-end with no network.
- [ ] `internal/orchestrator`: pure `Resolver` (table-tested over all `(phase, event)`), `Worker`
      executing every `Decision` branch, `Brain` port + §9 result types, transcript builder.
- [ ] Orchestrator imports only `board` + `forge` + its own `Brain` port (`imports_test.go` green).
- [ ] `internal/queue`: per-card keyed-mutex serialization + concurrent distinct cards + cross-restart
      TTL lock that self-heals.
- [ ] `internal/server`: receiver validates via `board.ParseEvent`, drops ignore/bot/duplicate events,
      enqueues survivors by `CardID`; `httptest`-covered.
- [ ] `internal/store`: `deliveries` (dedupe), `locks` (TTL), and `CardRecord.LastProcessedCommentID`
      in both bbolt and memory impls; existing tests still green.
- [ ] `wazir serve --addr` boots the receiver → queue → worker (faked brain) over the configured board
      and drains on shutdown.
- [ ] Idempotency proven: a replayed delivery resolves a card exactly once.
- [ ] `go test ./...` green (no network/credentials); `go vet ./...` clean.
