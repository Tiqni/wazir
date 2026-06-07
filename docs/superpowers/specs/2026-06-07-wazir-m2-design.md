# Wazir M2 — Claude Runner + Live Brainstorm Loop (Design Spec)

**Date:** 2026-06-07
**Status:** Approved for planning
**Scope:** Milestone **M2 + M3 collapsed** (see `docs/wazir-init-plan.md` §10): the real `claude`-CLI
runner *and* the real brainstorm loop, live on the board. Plan/execute (worktrees) stay M4; cost
persistence, label approval, and guardrail hardening stay M5.
**Source of truth:** `docs/wazir-init-plan.md` (§8.4 Claude Runner, §9 output contracts, §11 config,
§12 gotchas) and the shipped M0/M1 specs + code. Where the init plan and the shipped code disagree,
the code wins. The init plan §8.4 envelope sketch is **wrong for the installed CLI** — see §2 below.

This spec turns M2 into a buildable, testable slice. It records the brainstorming decisions, the
empirical CLI findings that drove them, and the design that follows. It does not restate the
architecture — read the init plan and the M0/M1 specs for that.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| M2 boundary | **Collapse M2 + M3**: runner machinery **and** the real brainstorm loop, live on the board | Demo ends with a real idea card iterating to an approved spec entirely on GitHub, driven by the live `claude` CLI. |
| Brain ↔ Superpowers | **Wazir-owned headless prompts; no plugin dependency** | The interactive `/superpowers:brainstorming` skill does **not** work headless (proved — §2). We author our own `--append-system-prompt` that forces the §9 JSON contract and disallows interactive tools. Superpowers need not be installed/enabled in the runner's cwd. Revisit the real `/superpowers:write-plan` & `execute-plan` commands in M4 where they add value. |
| Plan/execute reach | **Brainstorm live; plan/execute deferred to M4** | `ClaudeBrain.Plan/Execute` return a sentinel `ErrPhaseRequiresWorktree`; the Worker treats it as a *friendly* deferral (post "spec approved — planning lands in M4", leave the card in `Planning`). No tool-enabled `claude` runs without worktree isolation (§12 prompt-injection). |
| Context strategy | **Reconstruct the transcript from the thread every turn** | The board is authoritative (design principle 2). Stateless, robust to restarts / out-of-order / concurrency. `session_id` persisted for diagnostics only, never for correctness. Resolves init-plan open question #2. Rejected: `--resume` as the primary mechanism (couples correctness to stored session state). |
| Cost guardrails | **Basic now** | `MAX_BRAINSTORM_TURNS` cap on the question loop + per-run `total_cost_usd`/`duration_ms`/`session_id` logged via zap + a per-card turn counter in the store. Deferred to M5: `runs` bucket, daily budget circuit-breaker, `/runs` endpoint. |
| Approval mechanism | **Column move** (`Spec Review → Planning`), not labels | Live `GetCard` reads the new Status as the approval — no new parsing. Label approval (`spec-approved`) stays M5. |
| Runner shape | **One generic `Runner` + a thin `ClaudeBrain`** | Single home for the fragile exec/envelope/contract logic; phases are just a prompt + a result struct. Rejected: per-phase runner types (3× duplication) and a generic `PhaseRunner[T]` (over-engineered for three phases). |

---

## 2. Empirical CLI findings (the experiments that drove the design)

Run against the installed `claude` **2.1.168** during brainstorming. These supersede the init plan §8.4
assumptions and must be re-verified when the CLI is upgraded (§12 CLI-drift).

1. **`--output-format json` returns a JSON _array_ of events, not a single envelope object.** Elements
   have a `type` of `system` | `assistant` | `user` | `rate_limit_event` | `result`. The final
   `type:"result"` element carries: `result` (final text), `session_id`, `total_cost_usd`, `num_turns`,
   `duration_ms`, `is_error`, `subtype` (`success` | `error_max_turns` | …), `stop_reason`, `usage`,
   `modelUsage`. The init plan's `json.Unmarshal(out, &envelope)` of a single object is wrong — the
   parser must walk the array and pick the result element.
2. **The raw `/superpowers:brainstorming <idea>` slash command does not work headless.** It follows its
   interactive flow: tries `AskUserQuestion` (which no-ops in `-p` mode, returning a stub `"Answer
   questions?"`), then falls back to **one** plain-prose question ("Question 1 of a few") and ends the
   turn. Output is human prose, one question at a time, **no machine-parseable contract**. (Also: the
   Superpowers plugin only loads when `claude`'s cwd resolves a project `.claude/settings.json` that
   enables it — it did **not** load from `/tmp`. Another reason not to depend on it.)
3. **A custom headless prompt produces a clean contract on the first try.** With
   `--append-system-prompt` ("ask ALL questions at once OR write the full spec; no interactive tools;
   end with exactly one fenced ```json block matching {phase,status,questions,spec_markdown}") and
   `--disallowedTools AskUserQuestion,…`, the model returned exactly one fenced ```json block,
   `status:"needs_answers"` with all questions batched into the array, empty `spec_markdown`, no stray
   prose — directly `json.Unmarshal`-able. One turn, ~24s, ~$0.17. No Superpowers needed.

---

## 3. Deliverable & demo

M2 ships the `internal/claude` package (real `claude`-CLI brain) wired into `wazir serve` in place of
`CannedBrain`, plus the M1-deferred live-GitHub seams the brainstorm loop needs. Brainstorm runs for
real against GitHub + the `claude` CLI; plan/execute are honest M4 deferrals.

**Demo (acceptance):**

1. **Live brainstorm loop on a real board.** A human writes an idea card and moves it to
   `Brainstorming`. Wazir runs a real `claude` brainstorm turn and either posts batched clarifying
   questions (→ `Awaiting Answers`) or writes the spec to the issue body (→ `Spec Review`). A human
   reply re-runs brainstorm with the updated thread; a human comment in `Spec Review` revises the spec;
   a human move to `Planning` is read as approval. The card iterates to an approved spec entirely on
   the board.
2. **Plan deferral.** On approval the card enters `Planning`; Wazir posts "spec approved — planning &
   build land in M4" and leaves it there (no `Failed`).
3. **Runner robustness (no network, fake binary).** `go test ./...` drives the `Runner` + `ClaudeBrain`
   against a `CLAUDE_BIN` fake binary across happy and failure envelopes.
4. **No loops.** The bot's own column moves and comments never re-trigger a turn.

---

## 4. Package layout (delta on M1)

```
✚ internal/claude/            # Runner (exec + JSON-array envelope + last-```json-block), ClaudeBrain (Brain impl), prompts
✎ internal/orchestrator/brain.go     # + BrainstormResult.Error, + a brainstorm "failed" status
✎ internal/orchestrator/worker.go    # brainstorm handles failed; turn counter + cap; friendly plan/execute deferral
✎ internal/orchestrator/transcript.go# TrimRight the body (stable separator now that prompt format matters)
✎ internal/board/github/board.go     # GetCard populates Phase from the item's Status
✎ internal/board/github/projects.go  # + ItemStatus seam (read an item's current Status option)
✎ internal/board/github/projects_gql.go # githubv4 impl of ItemStatus (integration-validated)
✎ internal/board/github/parse_event.go  # drop projects_v2_item events where sender == bot_login
✎ internal/config/config.go          # + claude section (bin, model, timeout, max_brainstorm_turns)
✎ internal/store/                     # + CardRecord.BrainstormTurns (+ ClaudeSessionID, diagnostics)
✎ cmd/wazir/serve.go                  # inject claude.New(...) brain; decouple drain ctx from signal ctx
```

**Dependency rule (load-bearing, init-plan §4.2/§5.1):** `internal/orchestrator` still imports only
`internal/board`, `internal/forge`, `internal/store`, and its own `Brain` port — never a provider
package. `internal/claude` implements the `Brain` port and is injected at `serve` wiring time; the core
never imports it. `imports_test.go` continues to enforce this. `internal/claude` imports neither a
board nor a forge provider — it speaks only the `Brain` port's domain types.

---

## 5. `internal/claude` — the runner

### 5.1 `Runner` (exec mechanics)

```go
type RunSpec struct {
    Prompt          string        // -p
    SystemPrompt    string        // --append-system-prompt
    Dir             string        // cmd.Dir (empty for brainstorm; worktree in M4)
    Model           string        // --model (empty = CLI default)
    Timeout         time.Duration // exec.CommandContext deadline
    DisallowedTools []string      // --disallowedTools
    AllowedTools    []string      // --allowedTools (empty for brainstorm)
    PermissionMode  string        // --permission-mode (default for brainstorm)
}

type RunResult struct {
    Text       string  // the result element's "result"
    SessionID  string
    CostUSD    float64
    NumTurns   int
    DurationMS int
    IsError    bool
    Subtype    string
}

func (r *Runner) Run(ctx context.Context, spec RunSpec) (RunResult, error)
```

`Run`:
1. Builds argv: `<bin> -p <Prompt> --append-system-prompt <SystemPrompt> --output-format json
   --permission-mode <mode> [--model <m>] [--allowedTools …] [--disallowedTools …]`. Sets `cmd.Dir`
   when `Dir != ""`.
2. `exec.CommandContext(ctx, …)` with the timeout; captures stdout + stderr separately.
3. Parses stdout: **`json.Unmarshal` into `[]map[string]json.RawMessage`** (or a typed event slice),
   finds the element with `type == "result"`, decodes it. **Defensive fallback:** if the top level is
   an object (older/newer CLI), decode it directly — so a shape change degrades gracefully.
4. Fails loudly (returns a non-nil error, stderr included) on: non-zero exit, `ctx` deadline,
   unparseable stdout, no `result` element, `is_error == true`, or `subtype != "success"`. The §12
   CLI-drift guard: never silently treat a malformed envelope as success.

### 5.2 Envelope + contract extraction

- `extractLastJSONBlock(text) (string, error)` — scans `text` for the **last physical line** equal to
  ` ```json ` and the next physical line equal to ` ``` `, returning the bytes between. Safe against
  fences inside `spec_markdown`: JSON-encoded strings escape newlines (`\n`), so an embedded fence
  never appears as its own physical line. Returns an error if no block is found (caller fails loudly).
- The phase result is `json.Unmarshal` of that block into the per-phase struct.

### 5.3 `ClaudeBrain` (implements `orchestrator.Brain`)

```go
func New(cfg config.ClaudeConfig, log *zap.Logger) *ClaudeBrain
```

- **`Brainstorm`** — assembles the brainstorm system prompt (§5.4), `RunSpec{Prompt: in.Transcript,
  SystemPrompt: …, DisallowedTools: interactive+fs+shell, PermissionMode: "default", Timeout, Model}`,
  calls `Runner.Run`, `extractLastJSONBlock`, unmarshals into the §9 brainstorm struct, maps to
  `BrainstormResult`. Any `Runner`/parse error → `BrainstormResult{Status: <failed>, Error: …}`
  (expected failures travel as the `failed` status; the `Worker` routes both a `failed` result and a
  non-nil Go error to `fail()`, so either is safe — §6). Logs `cost_usd`, `duration_ms`, `session_id`,
  `status`.
- **`Plan` / `Execute`** — return `ErrPhaseRequiresWorktree` (sentinel). Drafted prompt templates live
  in code comments / the M4 spec; not wired to run.

### 5.4 Brainstorm prompts (validated in §2.3)

- **System** (`--append-system-prompt`): *"You are the BRAINSTORM phase of an automated, human-gated
  dev-loop orchestrator. No live human is reachable this turn. You receive an issue transcript. Do NOT
  use AskUserQuestion or any interactive/file/shell tool. In ONE response either (a) ask ALL clarifying
  questions at once if the idea needs them, or (b) write a complete implementation spec in markdown if
  it is clear enough. End with EXACTLY ONE fenced ```json block and nothing after it, matching:
  `{"phase":"brainstorm","status":"needs_answers"|"spec_ready","questions":[...],"spec_markdown":"..."}`.
  Put ALL human-facing prose inside the JSON fields."* (Kept as a Go constant; the exact text is part
  of the deliverable and the place to tune brainstorm quality.)
- **User** (`-p`): `orchestrator.BuildTranscript(card)`.
- **Tools:** `--disallowedTools AskUserQuestion,Bash,Edit,Write,Task,WebFetch,WebSearch`,
  `--permission-mode default`, no `--allowedTools`. Brainstorm needs no tools.

---

## 6. Orchestrator changes (`internal/orchestrator`)

- **`BrainstormResult` gains a failure channel** (M1 follow-up): add `Error string` and a brainstorm
  `failed` status constant. `Worker.brainstorm` switch handles `failed` → `w.fail(...)`.
- **Turn counter + cap.** `CardRecord.BrainstormTurns`. On `needs_answers`: increment + persist before
  moving to `Awaiting Answers`; if the count reaches `MAX_BRAINSTORM_TURNS`, instead post "I've hit the
  question limit (N) — this card needs a human to decide." and leave it in `Awaiting Answers` (no spend,
  no `Failed`). On `spec_ready`: reset to 0.
- **Friendly plan/execute deferral.** `Worker.plan` (and `executePhase`) recognize
  `claude.ErrPhaseRequiresWorktree`: post "✅ Spec approved — planning & build land in M4." and return
  nil **without** moving to `Failed` (card stays in `Planning`). Keeps the demo clean. (The existing
  forge `ErrNotImplemented` Failed path is unaffected and still covers M4 wiring gaps.)
- **`BuildTranscript`** (M1 follow-up): `strings.TrimRight(c.Body, "\n")` so the body→thread separator
  is deterministic.

---

## 7. Live-GitHub changes (`internal/board/github`)

The M1-deferred seams the live loop needs:

- **`GetCard` populates `Phase`.** Add `projectsAPI.ItemStatus(ctx, projectID, issueNodeID)
  (optionID string, found bool, err error)` (githubv4 in `projects_gql.go`, fake in tests). `GetCard`
  looks it up and reverse-maps `optionID → Phase` via the cached `BoardRecord.Options`
  (`map[Phase]optionID`, inverted). Unknown/empty Status → empty `Phase` (resolver → `ActNone`, safe).
  This is the single most important fix: without it the live resolver returns `ActNone` for everything.
- **Self-move loop prevention in `ParseEvent`.** For `*github.ProjectV2ItemEvent`, drop the event when
  `e.GetSender().GetLogin() == b.botLogin` (the bot's own `MoveTo` mutations emit these; without the
  filter each bot move re-triggers the worker — e.g. re-running brainstorm). **Requires `bot_login`
  configured**; document it as a M2 operational prerequisite. (Comment self-events are already filtered
  by `bot_login` + the marker.)
- **Status-changed-only guard — deferred to M5 (not cheap in go-github v66).** `ProjectV2ItemChange`
  exposes only `ArchivedAt` — **not** the changed-field id or new value — so the `projects_v2_item`
  webhook can't distinguish a Status change from another field edit, and `Event.NewPhase` **cannot** be
  populated from the payload. M2 therefore relies entirely on live `GetCard` for the authoritative
  phase; the resolver's `ev.NewPhase` checks never fire for GitHub `projects_v2_item` events (harmless —
  the phase cases cover every transition). The self-move filter removes the dominant loop source.
  Accepted M2 risk: a human editing an unrelated field on an in-flight card re-runs brainstorm — rare,
  mitigated in M5.
- **Approval is the column move** — no label parsing. `GetCard` returning `Planning` after a human drag
  is the approval signal the resolver already handles (`case PhasePlanning → ActPlan`). (Verified:
  `ProjectV2ItemEvent.GetSender()` exists in go-github v66, so the self-move filter above is sound.)

---

## 8. Config (`internal/config`) — new `claude` section

```yaml
claude:
  bin: claude              # WAZIR_CLAUDE_BIN — path to the binary (injectable for tests)
  model: ""                # WAZIR_CLAUDE_MODEL — empty = CLI default
  timeout: 5m              # WAZIR_CLAUDE_TIMEOUT — per-invocation deadline
  max_brainstorm_turns: 8  # WAZIR_CLAUDE_MAX_BRAINSTORM_TURNS — cap on the question loop
```

`ClaudeConfig` with fig tags + `default:` tags (`bin: "claude"`, `timeout: "5m"`,
`max_brainstorm_turns: 8`). No new required fields, no new validation failure modes. `time.Duration`
parses via fig's string→duration (verify; else parse a string field).

---

## 9. `wazir serve` wiring (`cmd/wazir/serve.go`)

- Replace `orchestrator.CannedBrain{}` with `claude.New(cfg.Claude, logger)` at the one injection line.
- **Decouple the drain context from the signal context** (M1 follow-up): the queue workers must run on
  a `context.Background()`-derived context (with a drain timeout on shutdown), not the SIGINT-cancelled
  `ctx`. Otherwise a graceful drain cancels an in-flight ~30s `claude` call mid-turn and routes the
  draining card down the failure path. The HTTP server keeps using the signal ctx to stop accepting;
  the queue gets its own lifecycle so in-flight turns finish.

---

## 10. Error handling summary

| Failure | Handling |
|---|---|
| `claude` non-zero exit / timeout / unparseable stdout / `is_error` / bad subtype | `Runner.Run` returns error (stderr included) → `BrainstormResult{failed, Error}` → `Worker.fail` → `⚠️` comment + `Failed`. |
| Missing / unparseable fenced ```json block | Same as above — fail loudly (CLI-drift §12). |
| Brainstorm question loop exceeds `MAX_BRAINSTORM_TURNS` | Post "hit the question limit" comment; leave in `Awaiting Answers`. No `Failed`, no further spend. |
| Plan/execute reached (approval) | `ErrPhaseRequiresWorktree` → friendly "planning lands in M4" comment; stays in `Planning`. |
| Bot's own move / comment | Dropped in `ParseEvent` (sender / marker) — no re-trigger. |
| Drain during in-flight turn | Queue runs on a decoupled drain context; the turn finishes within the drain timeout. |

---

## 11. Testing strategy

- **Fake `claude` binary** via `CLAUDE_BIN` — the Go `TestHelperProcess` pattern (re-exec `os.Args[0]`
  with `-test.run=TestHelperProcess` and an env switch) so no shell script ships and it's cross-platform.
  The helper prints a canned JSON-array envelope to stdout (and can sleep / exit non-zero / write
  stderr on demand).
- **`Runner` unit tests:** happy path (array → result text → fenced block); embedded/escaped fences in
  `spec_markdown`; missing block; malformed JSON; `is_error:true`; non-`success` subtype; non-zero exit
  (stderr surfaced); timeout (helper sleeps past the deadline); defensive single-object fallback.
- **`ClaudeBrain` tests:** canned envelopes → `BrainstormResult` for `needs_answers` (batched
  questions), `spec_ready` (body), and `failed`; argv assembled correctly (prompt, system prompt,
  disallowed tools, model).
- **`board/github` tests:** `GetCard` Status→Phase via the `projectsAPI` fake (known option, unknown
  option, no item); `ParseEvent` drops `sender == bot_login` `projects_v2_item` events (new
  testdata fixture); existing tests stay green. The build-tagged integration test remains the live
  guard and gains `ItemStatus`/phase coverage.
- **`orchestrator` tests:** brainstorm `failed` path; turn counter increments / caps / resets; friendly
  plan deferral leaves the card in `Planning`.
- **`config` tests:** `claude` section defaults + `WAZIR_CLAUDE_*` env overrides.
- All of `go test ./...` stays network- and credential-free. One optional **manual** end-to-end
  (real board + real `claude`) is documented in `docs/m0-setup.md`-style notes, not in CI.

---

## 12. Out of M2 scope (M4 / M5)

Deferred as deliberate seams, not gaps:

- **Plan/execute live** (`/superpowers:write-plan` & `execute-plan` runner paths, worktree `cmd.Dir`,
  tool allowlists, push/PR) — **M4**. M2 returns `ErrPhaseRequiresWorktree`.
- **Label-based approval** (`spec-approved`) parsing — **M5**. M2 uses the column move.
- **`runs` bucket + per-run cost persistence, daily budget circuit-breaker, `/runs` status endpoint** —
  **M5**. M2 logs cost via zap and caps brainstorm turns.
- **Prompt-injection guardrails / permission scoping beyond the brainstorm tool-disable, retries +
  backoff** — **M5**.
- **`--resume` session optimization** — out; the thread is the source of truth. `session_id` is stored
  for diagnostics only.
- **Status-changed-only event guard** — **M5** (go-github v66's `ProjectV2ItemChange` exposes no
  changed-field id; §7).

---

## 13. Acceptance checklist

- [ ] `internal/claude`: `Runner` execs `CLAUDE_BIN`, parses the JSON-**array** envelope to the result
      element, extracts the last fenced ```json block, fails loudly on every malformed/error case.
- [ ] `ClaudeBrain` implements `Brain`; `Brainstorm` live (needs_answers / spec_ready / failed);
      `Plan`/`Execute` return `ErrPhaseRequiresWorktree`.
- [ ] `BrainstormResult` has `Error` + a `failed` status; `Worker` handles it.
- [ ] `MAX_BRAINSTORM_TURNS` cap enforced via `CardRecord.BrainstormTurns`; cost/session/duration
      logged via zap.
- [ ] Friendly plan deferral: approval → "planning lands in M4" comment, card stays in `Planning`.
- [ ] `GetCard` populates `Phase` from the item's Status (via `projectsAPI.ItemStatus`).
- [ ] `ParseEvent` drops `projects_v2_item` events where `sender == bot_login`.
- [ ] `config` has a `claude` section with `WAZIR_CLAUDE_*` env overrides.
- [ ] `wazir serve` injects `claude.New(...)` and runs the queue on a drain context decoupled from the
      signal context.
- [ ] `internal/orchestrator` still imports no provider package (`imports_test.go` green).
- [ ] `go test ./...` green (no network/credentials); `go vet ./...` clean; `go test -race ./...` clean.

---

## 14. Operational prerequisites (M2 live run)

- `claude` binary installed and authenticated on the box; `WAZIR_CLAUDE_BIN` if not on `PATH`.
- `bot_login` **must** be set (self-move loop prevention depends on it).
- The board provisioned (`wazir provision` / `bootstrap`) so Status options ↔ Phase are cached.
- GitHub App/PAT subscribed to `issues`, `issue_comment`, `projects_v2_item`; webhook reachable.
- Cost: `claude -p` draws from the metered Agent SDK credit (§12). `MAX_BRAINSTORM_TURNS` is the only
  brake until the M5 budget breaker.
