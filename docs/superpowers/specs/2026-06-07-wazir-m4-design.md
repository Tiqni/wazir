# Wazir M4 — Worktree + Plan + Execute + PR (Design Spec)

**Date:** 2026-06-07
**Status:** Approved for planning
**Scope:** Milestone **M4** (`docs/wazir-init-plan.md` §10): the live plan + execute path. A human-approved
spec produces a real PR. Worktree creation via the `CodeForge` port, real `claude` plan/execute turns
inside the worktree, branch push, PR open, card → `PR Review`. Hardening (container isolation, daily
budget breaker, retries, worktree GC, label approval, `/runs`) stays **M5**.
**Source of truth:** `docs/wazir-init-plan.md` (§8.4 Claude Runner, §8.6 Worktree Manager, §9 plan/execute
contracts, §11 config, §12 gotchas), the shipped M0/M1/M2 specs + code, and the M2 spec's runner
machinery (`internal/claude`). Where the init plan and the shipped code disagree, the code wins.

This spec turns M4 into a buildable, testable slice. It records the brainstorming decisions and the
design that follows. It does not restate the architecture — read the init plan and the M0/M1/M2 specs.

> **Note (numbering):** the init plan's M3 (real brainstorm loop) was collapsed into the shipped M2
> (`2026-06-07-wazir-m2-design.md`). M4 is the next genuinely-unbuilt milestone; the worker's
> `plan → executePhase → PushBranch → OpenPR → PR Review` skeleton already exists from M1/M2 — M4 fills
> the two holes (forge git ops + live plan/execute brain) and adds the worktree lifecycle around them.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| Plan/execute brain prompt | **Spike first, then decide** | A throwaway experiment (Task 0, §7) compares the real `/superpowers:write-plan` & `execute-plan` slash commands against Wazir-owned plan/execute prompts in a real worktree, mirroring M2 §2. The brain is structured to accept either; the spike picks it before Tasks 3–4 and records findings spec §7-style. |
| Git auth for clone + push | **Reuse the configured PAT via `-c http.extraHeader`** | `AUTHORIZATION: basic base64("x-access-token:"+token)` passed per `git` invocation. Never persisted to `.git/config` or a remote URL. One credential source shared with the REST/GraphQL clients. App-token git-auth scaffolded behind the seam (M4 ships PAT, like M0). |
| Execute security posture | **Pragmatic least-privilege now; container in M5** | Tight `--allowedTools`, `--permission-mode acceptEdits`, **never** `--dangerously-skip-permissions`, and a **curated `cmd.Env`** that drops `WAZIR_*` host secrets. Container/HOME isolation + budget breaker → M5. Leans on the existing human spec-approval gate, which already precedes any code execution. |
| Worktree tooling | **Direct `git worktree` via `os/exec` in the forge** | Claude-Code worktree support and Worktrunk (`wt`) were considered and **rejected for the orchestrator layer** (§3.1). Both target *interactive* developer workflows; the headless daemon needs a deterministic, fully-owned, dependency-light VCS surface. Adopting either would add an external CLI dependency + out-of-band config/hooks (hidden state, against design principle 2) and would invert principle 3 (model must not own VCS state). They remain fine for working on Wazir's *own* repo — an orthogonal personal choice. |
| Worktree cleanup timing | **Remove on success; keep on failure for debugging** | After the PR opens, `RemoveWorktree`. On any failure, leave the worktree + log its path. Worktree GC of stale/failed trees → M5. *Rejected: remove on a `Done` event — the worker doesn't act on `Done` yet.* |
| Commit ownership | **Spike-decided; default: `claude` commits, orchestrator pushes** | The worker creates the branch (`git worktree add -b`); `claude` commits onto it during execute (matches `execute-plan`); the worker pushes that branch and opens the PR. `ExecuteResult.Branch/Commits` already model this. The spike confirms whether execute commits on its own. |
| Branch naming | **Orchestrator-owned, deterministic** | The worker computes `feature/issue-<n>-<slug(title)>` and creates the worktree with `-b`. `ExecuteResult.Branch` is confirmatory only — the worker pushes its own branch name (principle 3). |
| `CodeForge` port shape | **Refined so the forge owns filesystem layout** | The M0 sketch passed `dest`/`path` in from the caller. M4 makes clone/worktree roots **forge-internal** so the provider-free core never holds filesystem-layout config (§4). |

---

## 2. Deliverable & demo

M4 makes `internal/forge/github` and `internal/claude`'s `Plan`/`Execute` live, and adds the worktree
lifecycle to `internal/orchestrator/worker.go`. The plan/execute machinery from M2 (`Runner`, envelope
parse, last-```json-block extraction, `--append-system-prompt` contract) is reused as-is.

**Demo (acceptance):**

1. **Approved spec → real PR.** A human moves a `Spec Review` card to `Planning`. Wazir clones (or
   fetches) the card's repo, creates `feature/issue-<n>-<slug>` in a fresh worktree, runs a real
   `claude` **plan** turn (writes the plan), then a real **execute** turn (edits code + commits in the
   worktree), pushes the branch, opens the PR, posts the link as a comment, and moves the card to
   `PR Review`. The worktree is removed.
2. **Honest failure.** A plan/execute failure, a push failure, or a worktree error posts a `⚠️` comment
   and moves the card to `Failed`; the worktree is left in place for inspection (path logged).
3. **Robustness (no network, fakes).** `go test ./...` drives the forge against a **local bare repo**
   (real `git`, no network) and the brain against a `CLAUDE_BIN` fake, across happy and failure paths.
4. **No secret leakage.** The `claude` subprocess runs with a curated env; `WAZIR_*` secrets are not in
   it, and the PAT never lands in any worktree's `.git/config`.

---

## 3. Worktree tooling — decision rationale (recorded per request)

The orchestrator manages worktrees with **direct `git worktree` via `os/exec`**, inside the
`CodeForge` port. Two alternatives were evaluated and rejected **for the orchestrator layer**:

- **Claude-Code worktree support** isolates an *interactive* agent's own work. Wazir inverts the flow:
  the orchestrator must create the worktree *first*, then invoke `claude -p` with `cmd.Dir` set to it,
  because the orchestrator owns the branch name, the path, the card↔worktree mapping (persisted in the
  store), and cleanup. Letting `claude` manage the worktree would violate principle 3 (*the model
  reasons; the orchestrator acts*) and doesn't map onto a headless `-p` invocation.
- **Worktrunk (`wt`)** is a developer-ergonomics CLI (hooks, `wt.toml`, commit-message generation). For
  a webhook-driven daemon it adds a hard external dependency heavier than `git` (already the floor),
  introduces out-of-band config/hooks the orchestrator doesn't own (hidden state, against principle 2),
  carries its own CLI-drift/version-pin tax (§12), and complicates the auth (`-c http.extraHeader`) and
  hermetic-test (local bare repo) stories we adopt below.

`git worktree add`/`remove` is ~3 `os/exec` calls — minimal, deterministic, fully owned, and trivially
testable against a local repo. If per-repo post-create *setup* is ever needed, the clean seam is a small
configurable post-create hook (a shell command from config), not adopting an external tool — and even
that is deferred to M5. **Layer note:** this decision is about the orchestrator creating worktrees for
cards. Working on Wazir's *own* codebase with `wt` or Claude-Code worktrees is orthogonal and unaffected.

---

## 4. Package layout (delta on M2)

```
✎ internal/forge/forge.go            # refine the port: EnsureClone(repo); CreateWorktree(repo,branch)->path; paths forge-internal
✎ internal/forge/github/forge.go     # live Clone/fetch, worktree add/remove, push — git via os/exec + http.extraHeader auth
✚ internal/forge/github/git.go       # thin git exec helper (argv, -c http.extraHeader, curated env, stderr capture)
✎ internal/orchestrator/brain.go     # PlanInput.WorktreePath, ExecuteInput.WorktreePath
✎ internal/orchestrator/worker.go    # worktree lifecycle: EnsureClone+CreateWorktree before plan; thread path; RemoveWorktree on success; base from config
✎ internal/claude/brain.go           # Plan/Execute run claude in the worktree (cmd.Dir), emit §9 contracts; stop returning the sentinel
✎ internal/claude/runner.go          # curated cmd.Env (drop WAZIR_* secrets); per-phase timeouts already supported via RunSpec.Timeout
✎ internal/store/store.go            # CardRecord.WorktreePath, CardRecord.Branch
✎ internal/config/config.go          # + forge section (git_bin, clone_root, worktree_root, base_branch); claude plan/execute timeouts + execute_allowed_tools
✎ cmd/wazir/serve.go                 # construct the forge with auth + roots; thread base branch into the worker
✎ wazir.example.yaml, CLAUDE.md      # document the forge config + M4 status
```

**Dependency rule (load-bearing, init-plan §4.2/§5.1):** `internal/orchestrator` still imports only
`internal/board`, `internal/forge`, `internal/store`, and its own `Brain` port — never a provider
package. The worktree **path** is just a string threaded from `forge` (return value) into `Brain`
(`WorktreePath`); the core holds no filesystem-layout config. `imports_test.go` continues to enforce
the rule.

---

## 5. The `CodeForge` port (refined) + GitHub impl

### 5.1 Refined port

The M0 sketch passed `dest`/`path` in from the caller; M4 makes the forge own its filesystem layout so
the provider-free core never carries clone/worktree roots:

```go
type CodeForge interface {
    EnsureClone(ctx context.Context, repo string) error                       // clone if absent, else fetch; dest is forge-internal
    CreateWorktree(ctx context.Context, repo, branch string) (path string, err error) // git worktree add -b branch from base; returns the path
    RemoveWorktree(ctx context.Context, path string) error                    // git worktree remove + prune
    PushBranch(ctx context.Context, repo, branch string) error                // push branch to origin
    OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, err error) // unchanged (live since M0)
}
```

Updating the interface ripples to: the GitHub stub methods (currently `ErrNotImplemented`), and the
`fakeForge` in `worker_test.go` (record calls + return a temp path).

### 5.2 GitHub forge implementation (`internal/forge/github`)

- **Construction.** `New(rest *github.Client, opts Options)` where `Options{GitBin, CloneRoot,
  WorktreeRoot, Base, Token string}`. `Token` is the PAT today (App installation token scaffolded —
  a `gitToken()` seam returns the PAT for `auth: pat`, errors for `auth: app` until wired, mirroring
  M0's `ErrAppAuthNotWired`).
- **Git helper (`git.go`).** All git runs go through one helper: `exec.CommandContext(ctx, gitBin,
  "-c", "http.extraHeader=AUTHORIZATION: basic "+b64, args...)`, `cmd.Dir` set, **curated env** (§6.3),
  stdout/stderr captured, fails loudly with stderr on non-zero exit. The `http.extraHeader` arg is only
  added for network ops (clone/fetch/push); local ops (worktree add/remove) skip it.
- **Layout.** Clone dir = `<CloneRoot>/<owner>/<name>`; worktree dir = `<WorktreeRoot>/<owner>-<name>-<branch-slug>`.
- **`EnsureClone`** — if the clone dir has a `.git`, `git -C <clone> fetch origin --prune`; else
  `git clone <https-url> <clone>`. Idempotent.
- **`CreateWorktree`** — `git -C <clone> worktree add <path> -b <branch> origin/<Base>`; returns `path`.
  (Fetch in `EnsureClone` keeps `origin/<Base>` current.)
- **`RemoveWorktree`** — `git -C <clone-or-path> worktree remove --force <path>` then
  `git worktree prune`. (Derives the clone from the path, or `--force` from the worktree itself.)
- **`PushBranch`** — `git -C <clone> push origin <branch>`. Worktrees share the clone's refs, so the
  branch the worktree committed to is pushable from the clone.
- **`OpenPR`** — unchanged (go-github `PullRequests.Create`).

---

## 6. `internal/claude` — live Plan/Execute

### 6.1 Inputs

`orchestrator.PlanInput` and `ExecuteInput` gain `WorktreePath string`. The brain sets `RunSpec.Dir =
in.WorktreePath` so `claude` runs inside the worktree.

### 6.2 Plan & Execute turns

The exact prompt strategy (real Superpowers slash commands vs Wazir-owned prompts) is **decided by the
Task 0 spike (§7)**; the structure below holds either way.

- **`Plan`** — `RunSpec{Prompt: BuildTranscript+spec, SystemPrompt: planSystemPrompt, Dir: WorktreePath,
  Timeout: PlanTimeout, AllowedTools: ["Read","Grep","Glob","Write","Edit"], PermissionMode:
  "acceptEdits"}`. No arbitrary `Bash` — planning explores + writes the plan file, it does not run code.
  Parse the §9 plan contract (`{phase:"plan", status:"plan_ready"|"failed", plan_path, summary, error}`)
  → `PlanResult`. A missing/unparseable contract or `is_error` → `PlanResult{Status: StatusFailed,
  Error}` (the worker routes it to `fail`).
- **`Execute`** — `RunSpec{Prompt: BuildTranscript+plan ref, SystemPrompt: executeSystemPrompt, Dir:
  WorktreePath, Timeout: ExecuteTimeout, AllowedTools: ExecuteAllowedTools, PermissionMode:
  "acceptEdits"}`. Parse the §9 execute contract (`{phase:"execute", status:"complete"|"failed",
  branch, commits, test_summary, notes, error}`) → `ExecuteResult`.
- Both **stop returning `ErrPhaseRequiresWorktree`**. The sentinel + the worker's friendly-deferral
  branches stay in the tree (harmless) until removed in a tidy-up, or are removed in Task 5 — the plan
  decides; tests assert the live path, not the sentinel.

### 6.3 Curated env (security)

`Runner.Run` sets `cmd.Env` to an **allowlist**, not the inherited process env: `HOME`, `PATH`, the
`claude` CLI's own auth vars, `LANG`/locale — and explicitly **excludes** `WAZIR_*` (PAT, webhook
secret, etc.). Host credentials never reach the model or any tool it spawns. Applied to **all** phases
(brainstorm included) since it is strictly safer and brainstorm already needs no host env.

### 6.4 Execute tool allowlist

`ExecuteAllowedTools` defaults to a Go-oriented set: `Read,Edit,Write,Bash(git:*),Bash(go:*),
Bash(gofmt:*),Bash(ls:*),Bash(cat:*)`. It is config (`claude.execute_allowed_tools`) so it can be
tuned without a rebuild. Per-repo / multi-language allowlists are **M5**. The set is deliberately tight:
no network tools, no unscoped `Bash`, no `WebFetch`/`WebSearch`.

---

## 7. Task 0 — the spike (resolves the brain prompt + commit ownership)

A throwaway experiment, **not** merged code, documented in this spec's revision (a `§2`-style "Empirical
findings" addendum, like the M2 spec) before Tasks 3–4 start. Against a **real cloned worktree** of a
scratch Go repo:

1. Run `/superpowers:write-plan` headless (`claude -p` with `cmd.Dir` = worktree). Record: does the
   Superpowers plugin **load** from the worktree's cwd (M2 found it didn't load from `/tmp`)? Does it
   emit a parseable plan? What does it write, and where?
2. Run `/superpowers:execute-plan` headless. Record: does it **edit files and commit** on its own
   (commit ownership)? Cost/time. Does it respect `--allowedTools` + `acceptEdits`?
3. Run Wazir-owned plan/execute prompts (the §6 shape) for comparison: contract cleanliness, cost/time.
4. **Decide** the prompt strategy and commit ownership; write the system-prompt constants accordingly;
   record the findings + the CLI version (§12 CLI-drift).

Acceptance for Task 0: a short written findings block + a go/no-go on Superpowers slash commands.

---

## 8. Orchestrator changes (`internal/orchestrator/worker.go`)

The skeleton already exists; M4 adds the worktree lifecycle and threads the path.

- **`plan`** (entered on approval, or re-entry while `Planning`):
  1. Compute `branch = "feature/issue-" + issueNumber + "-" + slug(card.Title)` (from the store record's
     `IssueNumber`; `slug` lowercases, keeps `[a-z0-9-]`, truncates).
  2. `forge.EnsureClone(repo)`; `path, _ := forge.CreateWorktree(repo, branch)`.
  3. Persist `Branch` + `WorktreePath` to `CardRecord` (so a `Building` re-entry can set `cmd.Dir`).
  4. `brain.Plan(PlanInput{Transcript, Spec: card.Body, WorktreePath: path})`.
  5. On `plan_ready`: persist `PlanPath`, move to `Building`, call `executePhase`.
- **`executePhase`** — `brain.Execute(ExecuteInput{Transcript, PlanPath, WorktreePath})`; on `complete`:
  `forge.PushBranch(repo, branch)` (the **worker's** branch, not `res.Branch`), `forge.OpenPR(...)`,
  post the link, move to `PR Review`, then `forge.RemoveWorktree(path)` (best-effort; log on error).
- **`ActExecute` re-entry** — already loads `rec.PlanPath`; now also load `rec.WorktreePath`/`rec.Branch`
  to rebuild `ExecuteInput` and the push target.
- **Failure** — the existing `fail()` path (comment + `Failed`) is unchanged. **Do not** remove the
  worktree on failure; log its path. (The friendly `ErrPhaseRequiresWorktree` deferral branches become
  dead once §6 goes live — removed in Task 5 or left inert; tests assert the live path.)
- **Base branch** — threaded from `config.Forge.BaseBranch` into the worker (replacing the hardcoded
  `base: "main"`), used for `OpenPR`. `CreateWorktree`'s base lives in the forge `Options`.

---

## 9. Store changes (`internal/store`)

`CardRecord` gains two fields (transparent serialization, no migration):

```go
WorktreePath string // M4: absolute worktree path, for Building re-entry cmd.Dir + cleanup
Branch       string // M4: feature/issue-<n>-<slug>, the deterministic push/PR branch
```

---

## 10. Config (`internal/config`) — new `forge` section + claude additions

```yaml
forge:
  git_bin: git                       # WAZIR_FORGE_GIT_BIN
  clone_root: ~/.wazir/clones        # WAZIR_FORGE_CLONE_ROOT — one persistent clone per repo
  worktree_root: ~/.wazir/worktrees  # WAZIR_FORGE_WORKTREE_ROOT — one worktree per card
  base_branch: main                  # WAZIR_FORGE_BASE_BRANCH — worktree base + PR base

claude:
  # ... existing bin/model/timeout/max_brainstorm_turns ...
  plan_timeout: 10m                  # WAZIR_CLAUDE_PLAN_TIMEOUT
  execute_timeout: 30m               # WAZIR_CLAUDE_EXECUTE_TIMEOUT (execute needs longer than brainstorm)
  execute_allowed_tools: "Read,Edit,Write,Bash(git:*),Bash(go:*),Bash(gofmt:*),Bash(ls:*),Bash(cat:*)"
```

`ForgeConfig` with fig + `default:` tags. `~` expands to `$HOME` at load (or document absolute paths).
`plan_timeout`/`execute_timeout` parse as `time.Duration` (same pattern the existing `claude.timeout`
uses). No new required fields; no new validation failure modes beyond non-empty roots.

---

## 11. `wazir serve` wiring (`cmd/wazir/serve.go`)

- Construct the forge with auth + roots:
  `f := forgegh.New(github.NewClient(hc), forgegh.Options{GitBin: cfg.Forge.GitBin, CloneRoot:
  cfg.Forge.CloneRoot, WorktreeRoot: cfg.Forge.WorktreeRoot, Base: cfg.Forge.BaseBranch, Token:
  cfg.GitHub.PAT})`.
- Thread the PR base into the worker (`WithBase(cfg.Forge.BaseBranch)` or a `NewWorker` param).
- Pass `cfg.Claude.PlanTimeout`/`ExecuteTimeout`/`ExecuteAllowedTools` into `claude.New`.
- No change to the queue/drain lifecycle — execute's longer timeout is bounded by `ExecuteTimeout`; the
  drain context (M2) already lets in-flight turns finish.

---

## 12. Error handling summary

| Failure | Handling |
|---|---|
| `EnsureClone` / `CreateWorktree` git error | `fail()` → `⚠️` comment + `Failed`. No worktree to keep (or partial — logged). |
| Plan/execute `claude` error, bad contract, `status:"failed"` | `PlanResult`/`ExecuteResult` `StatusFailed` → `fail()`. Worktree **left** for debugging (path logged). |
| `PushBranch` git error | `fail()`; worktree left (branch may be partially committed). |
| `OpenPR` REST error | `fail()`; branch already pushed (idempotent re-run can re-open). Worktree left. |
| Success | Comment PR link, → `PR Review`, `RemoveWorktree` (best-effort; log on error). |
| `Building` re-entry (re-delivered event / crash) | `ActExecute` reloads `PlanPath`+`WorktreePath`+`Branch` and re-runs execute. |
| Host secret exposure | Curated `cmd.Env` (§6.3) excludes `WAZIR_*`; PAT via per-invocation `http.extraHeader`, never in `.git/config`. |

---

## 13. Testing strategy

- **Forge (`internal/forge/github`):** drive real `git` against a **local bare repo as `origin`**
  (`git init --bare` in a temp dir, `file://` URL — no network). Cover `EnsureClone` (clone then
  idempotent fetch), `CreateWorktree` (branch created, path returned, files present),
  `PushBranch` (commit in the worktree lands in the bare origin), `RemoveWorktree` (tree gone, pruned).
  Unit-test the `http.extraHeader` arg + base64 construction separately (it doesn't apply to `file://`).
- **Brain (`internal/claude`):** fake `CLAUDE_BIN` (the M2 shell-script helper) that, for execute,
  writes a file + `git commit`s in `cmd.Dir` and prints a §9 execute envelope; for plan, prints a plan
  envelope. Assert argv carries `cmd.Dir`, the tool allowlist, `--permission-mode acceptEdits`, the
  per-phase timeout; assert the curated env excludes a sentinel `WAZIR_SECRET`.
- **Worker (`internal/orchestrator`):** extend `scriptedBrain` for plan/execute; a **recording fake
  forge** asserts the `EnsureClone → CreateWorktree → PushBranch → OpenPR → RemoveWorktree` sequence,
  that the **worker's** branch (not `res.Branch`) is pushed, that `Branch`/`WorktreePath` persist, and
  that a failure **skips** `RemoveWorktree`.
- **Config:** `forge` defaults + `WAZIR_FORGE_*`/`WAZIR_CLAUDE_*` env overrides; `plan_timeout`/
  `execute_timeout` parse.
- **Build-tagged manual integration (`-tags integration`, not in CI):** real clone + real `claude` +
  real push + real PR against a scratch repo, env-driven, skips when unset. The permanent live guard
  for the git + brain path (mirrors M0's live provisioning test).
- All of `go test ./...` stays network- and credential-free; `go vet ./...` clean; `go test -race ./...`
  clean.

---

## 14. Out of M4 scope (→ M5)

Deferred as deliberate seams, not gaps:

- **Container / HOME isolation** of the execute run, network-egress limits — **M5**. M4 ships curated
  env + tight tool allowlist + the human gate. **Cwd isolation is done:** worktree-less phases
  (brainstorm) run in a fresh temp dir, so claude can't inherit the daemon's cwd and auto-load an
  unrelated project `CLAUDE.md` / plugin config (a live-test bug — brainstorm was reasoning about
  Wazir's own repo when `serve` ran from it). Still **M5**: a per-run isolated `HOME`/`~/.claude` so the
  *global* `~/.claude/CLAUDE.md` and globally-enabled plugins also can't bleed into a turn. (Open
  follow-up, not M4: brainstorm currently has *no* repo context at all, so it asks foundational
  questions; a deliberately repo-aware brainstorm — feeding a target-repo summary — is a future option.)
- **Daily budget circuit-breaker, `runs` bucket + per-run cost persistence, `/runs` endpoint** — **M5**.
  M4 logs plan/execute cost via zap (reusing the M2 logging).
- **Retries / backoff** on transient git/PR errors — **M5**. M4 fails loudly to `Failed`.
- **Worktree GC** of stale/failed trees — **M5**. M4 keeps failed worktrees for debugging.
- **Per-repo / multi-language tool allowlists & build commands; post-create setup hook** — **M5/M6**.
  M4 uses one configurable Go-oriented allowlist.
- **App-token git-auth** — scaffolded; M4 ships PAT (like M0).
- **Label-based approval** (`spec-approved`) — **M5**; M4 (like M2) uses the column move.

---

## 15. Acceptance checklist

- [ ] **Task 0 spike** findings recorded (Superpowers-vs-owned + commit ownership + CLI version);
      prompt strategy chosen.
- [ ] `CodeForge` refined (`EnsureClone`, `CreateWorktree`→path, paths forge-internal); GitHub impl
      live (clone/fetch, worktree add/remove, push) via `git` `os/exec` + `http.extraHeader` auth.
- [ ] PAT never lands in any worktree `.git/config`; verified in a test.
- [ ] `claude.Plan`/`Execute` run in the worktree (`cmd.Dir`), emit the §9 plan/execute contracts, and
      no longer return `ErrPhaseRequiresWorktree`.
- [ ] `Runner` uses a curated `cmd.Env` that excludes `WAZIR_*`; execute uses the tight tool allowlist +
      `acceptEdits`, never `--dangerously-skip-permissions`.
- [ ] Worker creates the worktree before plan, persists `Branch`/`WorktreePath`, pushes its own branch,
      opens the PR, moves to `PR Review`, removes the worktree on success, keeps it on failure.
- [ ] `config` has a `forge` section + claude `plan_timeout`/`execute_timeout`/`execute_allowed_tools`
      with `WAZIR_*` overrides.
- [ ] `wazir serve` constructs the forge with auth + roots and threads the base branch.
- [ ] `internal/orchestrator` still imports no provider package (`imports_test.go` green).
- [ ] `go test ./...` green (no network/credentials); `go vet ./...` clean; `go test -race ./...` clean.
- [ ] Manual integration: an approved scratch-repo card produces a real PR end-to-end.

---

## 16. Operational prerequisites (M4 live run)

- `git` installed on the box; `WAZIR_FORGE_GIT_BIN` if not on `PATH`. `claude` installed + authenticated
  (its auth lives in `HOME`, which the curated env preserves).
- The PAT (or App, when wired) has **Contents: read/write** on each target repo (for clone + push) in
  addition to the M0–M2 scopes; the App must be installed on each repo whose cards run (§4.1).
- `clone_root` / `worktree_root` writable; enough disk for one clone per repo + one worktree per
  in-flight card.
- Cost: plan + execute `claude` turns draw from the metered Agent SDK credit (§12). `execute_timeout`
  bounds a single run; the daily budget breaker is **M5**.
- The human spec-approval gate (`Spec Review → Planning`) is the security boundary before any code runs.
