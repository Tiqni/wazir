# Wazir M5 (slice 1) — Execution Isolation (Design Spec)

**Date:** 2026-06-07
**Status:** Approved for planning
**Scope:** The first slice of milestone **M5** (`docs/wazir-init-plan.md` §10 "Hardening", §12 "Headless is
single-turn / isolated HOME per concurrent run", M4 spec §14 "Container / HOME isolation → M5"). Per-run
isolation of the headless `claude` brain so concurrent turns can't corrupt each other's session state and
the **global** `~/.claude` context (user memory, globally-enabled plugins, MCP) and the **orchestrator's
own** repo context can't bleed into a card's turn — while each turn *does* see its **target repo's**
context. Pure Go + `os/exec`; no container, OS sandbox, or network-egress control (those stay M6).
**Source of truth:** `docs/wazir-init-plan.md` and the shipped M0–M4 specs + code. Where the init plan and
the shipped code disagree, the code wins.

> **M5 is four independent slices, not one project.** This spec covers **execution isolation** only. The
> other three — *observability & cost* (runs bucket + per-run cost persistence + `/runs` endpoint + daily
> budget breaker), *resilience* (retry/backoff on transient git/PR errors + worktree GC), and *approval &
> auth ergonomics* (label-based approval + App-token git auth) — get their own spec → plan → implementation
> cycles. Items §10 lists under M5 that M0–M4 already shipped (the Failed column + error reporting,
> permission scoping, structured zap logging) are out of scope here.

This spec turns the slice into a buildable, testable unit. It records the brainstorming decisions and the
design that follows. It does not restate the architecture — read the init plan and the M0–M4 specs.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| Isolation depth | **Per-run config dir only; container/OS-sandbox/egress → M6** | Smallest cohesive slice, no new infra. Closes the two documented gaps (§12 "isolated HOME per concurrent run"; M4 §14 "global `~/.claude/CLAUDE.md`/plugins bleeding into a turn"). The human spec-approval gate already precedes any code execution; a container is the right *eventual* home for network-egress defense but drags in Docker + image build + auth-into-container plumbing — its own milestone. |
| The isolation primitive | **Relocate `CLAUDE_CONFIG_DIR`, not `HOME`** | The thing to isolate is `~/.claude` (global memory, enabled plugins, MCP, session state) — which *is* `CLAUDE_CONFIG_DIR`. Relocating `HOME` would also move `$HOME/go`, `$HOME/.cache/go-build`, `~/.gitconfig`, so every execute turn would rebuild cold. Keep the real `HOME`; point `CLAUDE_CONFIG_DIR` at a fresh per-run temp dir. A deliberate, reasoned divergence from §12's loose "isolated HOME" wording. |
| Mechanism | **Approach A: empty per-run `CLAUDE_CONFIG_DIR` + `--plugin-dir`** | An empty per-run config dir ⇒ no global `CLAUDE.md`, no globally-enabled plugins, no global MCP, isolated session state. Add back exactly what each phase needs: `--plugin-dir <superpowers>` for plan/execute, nothing for brainstorm. `CLAUDE_CONFIG_DIR` is well-established; `--plugin-dir` is the one newer flag to spike-verify. **Fallback (spike-selected): Approach C** — seed a minimal `settings.json` + symlink the plugin cache — if `--plugin-dir` proves unavailable. **Rejected: Approach B** (`--bare` everywhere) — it also strips the *target* repo's own `CLAUDE.md` during plan/execute, losing wanted project guidance. |
| Claude auth under isolation | **Long-lived subscription token (`CLAUDE_CODE_OAUTH_TOKEN`)** | This macOS box authenticates via Keychain (no `~/.claude/.credentials.json` exists), which may not survive a relocated config dir. A token from `claude setup-token` exported in the daemon env rides in via env precedence (ahead of the config dir), keeps subscription billing, and is portable to a Linux VPS. The curated env already passes `CLAUDE_*` through. (API key remains a documented alternative; it bills the separate metered Agent SDK credit, §12.) |
| Brainstorm context | **Repo-aware + read-only exploration** | cwd = the card's repo clone so its `CLAUDE.md`/`AGENTS.md` auto-load *and* `Read,Grep,Glob` are allowed so brainstorm can inspect real source (best for bug cards). Replaces the M4 "fresh temp cwd" hack, which suppressed the *orchestrator's* context but left brainstorm with *no* repo context at all (M4 §14 flagged this). Cost is bounded by the brainstorm timeout + the existing max-turns cap. |
| Context principle (whole slice) | **A card's turn sees its target repo's context and nothing else** | Suppress global user memory, the orchestrator's own repo, and unrelated plugins; provide the *target* repo's `CLAUDE.md`/`AGENTS.md`/source. Plan/execute already satisfy this (cwd = worktree); brainstorm is brought into line. |
| `EnsureClone` shape | **Returns the clone path** | `EnsureClone(ctx, repo) (clonePath string, err error)` so brainstorm can run with cwd = the clone. The path is a plain `string` — no provider type crosses the port, so `imports_test.go` stays green. |

---

## 2. Deliverable & demo

This slice makes every headless `claude` invocation run in a hermetic, per-run config dir, and makes
brainstorm repo-aware. All changes live in `internal/claude`, `internal/config`, `internal/forge`,
`internal/orchestrator`, and `cmd/wazir` — no store/board changes, no migration.

**Demo (acceptance):**

1. **No global bleed.** With a sentinel line planted in the real `~/.claude/CLAUDE.md` and an unrelated
   globally-enabled plugin, a brainstorm/plan/execute turn's output shows no awareness of either: the
   relocated empty `CLAUDE_CONFIG_DIR` hides both. (Verified manually in the Task 0 spike; unit-tested via
   the fake `CLAUDE_BIN` asserting `CLAUDE_CONFIG_DIR` points at a fresh empty dir.)
2. **Target-repo context present.** A brainstorm turn for a card in repo X loads X's `CLAUDE.md`/`AGENTS.md`
   and can `Read`/`Grep`/`Glob` X's source; plan/execute (cwd = worktree) already do.
3. **Parallel-safe.** Two concurrent turns get two distinct `CLAUDE_CONFIG_DIR`s; neither sees the other's
   session state. Each dir is removed when its run returns, even on error/timeout.
4. **Stays authenticated.** Runs authenticate from `CLAUDE_CODE_OAUTH_TOKEN` in the curated env; no Keychain
   dependency; the relocated config dir does not break auth.
5. **No secret leakage (unchanged from M4, re-asserted).** The curated env still drops `WAZIR_*` and keeps
   `CLAUDE_CODE_OAUTH_TOKEN`/`ANTHROPIC_*`; the real `HOME` is preserved so toolchain caches stay warm.
6. **Robustness (no network, fakes).** `go test ./...` drives the runner against a fake `CLAUDE_BIN` that
   echoes its env + argv, asserting the isolation invariants across happy and failure paths.

---

## 3. Architecture — one load-bearing primitive

Every `claude` invocation gets a **fresh, empty `CLAUDE_CONFIG_DIR`** (a per-run temp dir), created in
`Runner.Run`, injected into the curated env, and `defer`-removed when the run returns. An empty config dir
means:

- no global user `~/.claude/CLAUDE.md` (it lives under the config dir, which is now empty);
- no globally-enabled plugins (no `settings.json` → nothing enabled);
- no global MCP servers;
- isolated session state (each run's scratch/sessions live in its own dir → parallel-safe).

The real `HOME` is **kept**, so `$HOME/go`, `$HOME/.cache/go-build`, and `~/.gitconfig` stay warm across
runs. Auth rides in via `CLAUDE_CODE_OAUTH_TOKEN` in the curated env (precedence ahead of the config dir).
We then add back exactly what each phase needs.

### 3.1 Per-phase isolation matrix

| Phase | cwd | `CLAUDE_CONFIG_DIR` | `--plugin-dir` | AllowedTools | DisallowedTools | global ctx | target-repo `CLAUDE.md`/`AGENTS.md` |
|---|---|---|---|---|---|---|---|
| brainstorm | the card's repo **clone** | fresh per-run | — (none) | `Read,Grep,Glob` | `Bash,Edit,Write,Task,WebFetch,WebSearch,AskUserQuestion` | suppressed | **loads** |
| plan | the card's **worktree** | fresh per-run | Superpowers | `Read,Grep,Glob,Write,Edit` | — | suppressed | **loads** |
| execute | the card's **worktree** | fresh per-run | Superpowers | execute allowlist (config) | — | suppressed | **loads** |

A `--setting-sources` value (spike-pinned, configurable) keeps a repo's own `.claude/settings.json` from
*widening* our `--allowedTools` ceiling, while still letting its `CLAUDE.md` load as context. Brainstorm
loads no plugin: the shipped brainstorm uses a custom system prompt, not the Superpowers brainstorming
skill (M2 decision), so it needs no plugin — only repo context.

### 3.2 Why this is the cleanest lever

The relocated empty config dir is a *single* primitive that achieves global-context suppression, plugin
suppression, MCP suppression, and per-run session isolation at once — without the `--bare` flag (which is
newer and would over-suppress the target repo's own `CLAUDE.md` during plan/execute) and without seeding a
config dir (which couples to the plugin-registry on-disk layout). The only thing we *add* per phase is the
plugin (plan/execute) and the read-only tool allowlist.

---

## 4. Package layout (delta on M4)

```
✎ internal/forge/forge.go            # EnsureClone(ctx, repo) -> (clonePath string, err error)
✎ internal/forge/github/forge.go     # EnsureClone returns the clone dir
✎ internal/orchestrator/brain.go     # BrainstormInput.RepoPath
✎ internal/orchestrator/worker.go    # brainstorm(): EnsureClone (after the max-turns breaker) → cwd; plan() ignores the returned path
✎ internal/claude/brain.go           # brainstorm sets Dir=RepoPath + AllowedTools[Read,Grep,Glob]; plan/execute set PluginDir+SettingSources
✎ internal/claude/runner.go          # per-run CLAUDE_CONFIG_DIR (create/inject/defer-remove); RunSpec.PluginDir/SettingSources → --plugin-dir/--setting-sources
✚ internal/claude/plugin.go          # DiscoverSuperpowersPluginDir(home) — newest …/claude-plugins-official/superpowers/<ver>/
✎ internal/config/config.go          # claude.plugin_dir, claude.setting_sources
✎ cmd/wazir/serve.go                 # resolve plugin dir (config → discover → fail loud); warn if no token/api key
✎ wazir.example.yaml, CLAUDE.md      # document the new claude config + M5 isolation status
```

**Dependency rule (load-bearing, init-plan §4.2/§5.1):** `internal/orchestrator` still imports only
`internal/board`, `internal/forge`, `internal/store`, and its own `Brain` port. `EnsureClone` now returns a
clone **path**, but a path is a plain `string` — no provider concept crosses the port. The brainstorm cwd
is threaded as a string from `forge` (return value) into `Brain` (`RepoPath`), mirroring how the worktree
path already flows. `imports_test.go` continues to enforce the rule.

---

## 5. The `Runner` — per-run config dir + isolation flags (`internal/claude/runner.go`)

`RunSpec` gains:

```go
PluginDir      string // when set, append --plugin-dir <PluginDir> (plan/execute)
SettingSources string // when set, append --setting-sources <SettingSources> (the repo-settings guard)
```

`Run` changes:

- **Per-run config dir.** Before building `cmd`, `dir, err := os.MkdirTemp("", "wazir-cfg-")`;
  `defer os.RemoveAll(dir)`. Inject `CLAUDE_CONFIG_DIR=<dir>` into the curated env. A `MkdirTemp` failure is
  an infra error (the turn fails → `Failed`). `RemoveAll` failure is best-effort, logged — it does not fail
  the turn. The `defer` runs even on claude error/timeout, so no per-run dir leaks.
- **Flags.** Append `--plugin-dir <PluginDir>` when non-empty; append `--setting-sources <SettingSources>`
  when non-empty.
- **`curatedEnv()`** is unchanged in shape: it already keeps `CLAUDE_*` (so `CLAUDE_CODE_OAUTH_TOKEN`
  passes), `ANTHROPIC_*`, the real `HOME`/`PATH`/locale, and drops `WAZIR_*`. `CLAUDE_CONFIG_DIR` is added
  to the returned slice after `curatedEnv()` (or `curatedEnv()` takes the per-run dir as a parameter).
- **cwd.** Unchanged: `cmd.Dir = spec.Dir`. The existing "empty `Dir` → fresh temp cwd" fallback stays as a
  defensive net (after this slice, all real phases pass a `Dir`, so it should be unreachable — keep it so a
  future caller can't accidentally run in the daemon's cwd).

The config dir (`wazir-cfg-*`) and the worktree-less cwd fallback (`wazir-run-*`) are **two distinct** temp
dirs with two distinct lifetimes.

---

## 6. The brain (`internal/claude/brain.go`)

- `ClaudeBrain` gains `pluginDir string` and `settingSources string`, set from config in `New`.
- **`Brainstorm`** sets `RunSpec.Dir = in.RepoPath`, `AllowedTools = []string{"Read","Grep","Glob"}`, keeps
  the existing `DisallowedTools` and `PermissionMode: "default"`, and sets `SettingSources`. It does **not**
  set `PluginDir`. Contracts/parsing unchanged.
- **`Plan`/`Execute`** set `PluginDir = c.pluginDir` and `SettingSources = c.settingSources` on their
  `RunSpec`. Everything else unchanged.
- **`DiscoverSuperpowersPluginDir(home string) (string, error)`** (new, `internal/claude/plugin.go`):
  globs `<home>/.claude/plugins/cache/claude-plugins-official/superpowers/*/` and returns the highest
  semantic version, or an error if none is found. Used by `serve` only when `claude.plugin_dir` is unset.
  Discovery reads the **real** `~/.claude` (the relocation is per-run, inside `Run`).

---

## 7. Task 0 — the spike (resolves flag availability + the auth/brainstorm recipe)

A throwaway experiment, **not** merged code, **run by the user** (headless `claude` is metered), documented
in this spec's revision (a §1-style "Empirical findings" addendum, like the M2/M4 specs) **before** the
runner/brain tasks land. Validates against the pinned CLI version:

1. `claude --help` lists `--plugin-dir` and `--setting-sources` (and `--bare`, for the Approach-C
   go/no-go). *Non-metered.*
2. With `CLAUDE_CONFIG_DIR=$(mktemp -d)` (empty) and `CLAUDE_CODE_OAUTH_TOKEN` set, a trivial
   `claude -p "say hi" --output-format json` runs cleanly — confirms empty config dir + token auth, no
   interactive first-run/onboarding hang.
3. In a real clone, `CLAUDE_CONFIG_DIR=<empty> claude -p "/superpowers:write-plan …" --plugin-dir <sp>
   --output-format json` resolves the slash command from the relocated dir, and a planted sentinel in the
   real `~/.claude/CLAUDE.md` does **not** appear in the output.
4. **Brainstorm recipe:** with cwd = a clone that has a `CLAUDE.md` *and* an `AGENTS.md`, an empty relocated
   config dir, `--setting-sources <value>`, and `--allowedTools Read,Grep,Glob` (no plugin) — confirm both
   memory files load, the source is readable, and the global sentinel stays hidden. Confirm `AGENTS.md`
   auto-loads; if it does not, the plan adds an inject-`AGENTS.md`-into-the-transcript step.
5. **Settings guard:** confirm the chosen `--setting-sources` value stops a planted worktree
   `.claude/settings.json` `permissions.allow` from taking effect while the worktree `CLAUDE.md` still
   loads. If the CLI can't cleanly separate "context yes / project-settings no," record the residual and
   the chosen mitigation (the tight base `--allowedTools` remains the ceiling regardless).
6. Record + pin `claude --version`; decide **A** vs the **C** fallback; write the system-prompt/flag
   constants accordingly.

Acceptance for Task 0: a short written findings block + a go/no-go on `--plugin-dir`, the pinned
`--setting-sources` value, the `AGENTS.md` decision, and the CLI version.

---

## 8. Orchestrator changes (`internal/orchestrator`)

- **`BrainstormInput`** (`brain.go`) gains `RepoPath string`.
- **`worker.brainstorm`** — after the max-turns cost-breaker check (so we never clone for a card we're about
  to escalate to a human) and before `brain.Brainstorm`:
  1. `clonePath, err := w.forge.EnsureClone(ctx, card.Repo)` — on error, return it (→ `fail()`).
  2. Pass `RepoPath: clonePath` into `BrainstormInput`.
- **`worker.plan`** — `EnsureClone` now returns `(path, err)`; discard the path with `_,` (plan uses the
  worktree path from `CreateWorktree`). No other change.
- No change to `executePhase`, the failure path, or the Building re-entry — they already operate inside the
  worktree.

---

## 9. Config (`internal/config`) — claude additions

```yaml
claude:
  # ... existing bin/model/timeout/max_brainstorm_turns/plan_timeout/execute_timeout/execute_allowed_tools ...
  plugin_dir: ""                     # WAZIR_CLAUDE_PLUGIN_DIR — absolute path to the Superpowers plugin
                                     #   for plan/execute. Empty → serve auto-discovers the newest version.
  setting_sources: ""                # WAZIR_CLAUDE_SETTING_SOURCES — value passed to --setting-sources to
                                     #   stop a repo's .claude/settings.json widening tools. Spike-pinned.
```

`ClaudeConfig` gains `PluginDir`/`SettingSources` with fig + `default:` tags (both default empty). No new
required fields and no new validation failure modes in `config` itself — the "is the plugin dir resolvable"
check is a `serve`-startup concern (§10), so unit config tests stay network-free. Auth env
(`CLAUDE_CODE_OAUTH_TOKEN`) is read by the `claude` CLI from the environment, not by `config`.

---

## 10. `wazir serve` wiring (`cmd/wazir/serve.go`)

- **Resolve the plugin dir** before constructing the brain: if `cfg.Claude.PluginDir != ""` use it; else
  `claude.DiscoverSuperpowersPluginDir(os.Getenv("HOME"))`. If neither yields a directory that exists,
  **fail loudly**: `"superpowers plugin not found; set WAZIR_CLAUDE_PLUGIN_DIR"`. Pass the resolved absolute
  path into `claude.New` (via the config or a setter). Failing at startup — not mid-turn — keeps plan/execute
  from dying after a card has already moved to Planning.
- **Warn (not fail) if no auth env** is present: if neither `CLAUDE_CODE_OAUTH_TOKEN` nor
  `ANTHROPIC_API_KEY` is set, log a warning that headless runs will likely fail to authenticate under the
  relocated config dir.
- No change to the queue/drain lifecycle.

---

## 11. Error handling summary

| Failure | Handling |
|---|---|
| Per-run `CLAUDE_CONFIG_DIR` `MkdirTemp` error | Infra error from `Run` → phase fails → `fail()` (`⚠️` + `Failed`). |
| `CLAUDE_CONFIG_DIR` `RemoveAll` error (cleanup) | Best-effort; logged via zap; does **not** fail the turn. Runs on every path via `defer`, including claude error/timeout. |
| `EnsureClone` git error at Brainstorming | Returned from `brainstorm()` → `fail()`. (Same handling as the existing Planning-time clone error.) |
| Superpowers plugin dir unresolved | `serve` startup error (before listening) with a fix-it message. |
| Missing auth env | `serve` startup **warning**; the run still attempts and fails loudly with the CLI's auth error if truly unauthenticated (§12 fail-loud). |
| Repo `.claude/settings.json` tries to widen tools | Blocked by `--setting-sources`; the base `--allowedTools` is the hard ceiling regardless (spike-validated). |

---

## 12. Testing strategy

- **Runner (`internal/claude`):** a fake `CLAUDE_BIN` (the M2 shell-script helper, extended) that prints a
  valid JSON envelope whose `result` echoes selected env vars + argv. Assert:
  - `CLAUDE_CONFIG_DIR` is set, points at a dir that **exists during** the run, and is **removed after**;
  - two sequential/concurrent runs get **distinct** `CLAUDE_CONFIG_DIR`s;
  - `--plugin-dir <x>` present for plan/execute, **absent** for brainstorm; `--setting-sources <v>` present
    when configured;
  - the real `HOME` is **preserved** (not relocated);
  - `WAZIR_*` is **dropped** and `CLAUDE_CODE_OAUTH_TOKEN` is **kept** in the child env;
  - the per-run config dir is cleaned up even when the fake exits non-zero / on a forced timeout.
- **Brain (`internal/claude`):** assert brainstorm's argv carries the clone cwd + `Read,Grep,Glob` and no
  `--plugin-dir`; plan/execute carry `--plugin-dir`. `DiscoverSuperpowersPluginDir` against a temp fake
  `~/.claude/plugins/cache/...` tree with two versions → returns the newest; empty tree → error.
- **Worker (`internal/orchestrator`):** the recording `fakeForge` gets an `EnsureClone` that returns a temp
  path; assert `brainstorm()` calls `EnsureClone` and threads the path as `BrainstormInput.RepoPath`, and
  that `EnsureClone` is **not** called when the max-turns breaker fires.
- **Config:** `plugin_dir`/`setting_sources` defaults + `WAZIR_CLAUDE_*` env overrides.
- **Build-tagged manual integration (`-tags integration`, not in CI):** an env-driven run that points at a
  real Superpowers install + real token and confirms a headless turn authenticates and loads only the
  target repo's context. Skips when unset. (Complements the user-run Task 0 spike.)
- All of `go test ./...` stays network- and credential-free; `go vet ./...` clean; `go test -race ./...`
  clean (the race detector guards the concurrent-distinct-config-dir test).

---

## 13. Out of this slice's scope

Deferred as deliberate seams, not gaps:

- **Container / OS-sandbox isolation, network-egress limits** — **M6.** This slice ships the relocated
  config dir + curated env + tight tool allowlist + the human gate.
- **Per-repo clone locking.** Concurrent `EnsureClone` (`git fetch`) on the *same* repo from two cards can
  lock-contend — pre-existing in `plan()`, now also possible at Brainstorming. Belongs to the **resilience**
  M5 slice (a per-repo lock), not here.
- **Multi-plugin / per-repo plugin sets** — **M6.** One Superpowers `--plugin-dir` for all plan/execute.
- **Other M5 slices** — observability & cost, resilience, approval & auth ergonomics — separate specs.

---

## 14. Acceptance checklist

- [ ] **Task 0 spike** findings recorded (flag availability, pinned `--setting-sources` value, `AGENTS.md`
      decision, CLI version); A vs C fallback chosen.
- [ ] `Runner.Run` creates a fresh per-run `CLAUDE_CONFIG_DIR`, injects it, and `defer`-removes it on every
      path (including error/timeout); `RunSpec.PluginDir`/`SettingSources` → `--plugin-dir`/`--setting-sources`.
- [ ] `curatedEnv` keeps `CLAUDE_CODE_OAUTH_TOKEN`, preserves the real `HOME`, drops `WAZIR_*`; verified in a
      test.
- [ ] Brainstorm runs with cwd = the card's repo clone + `Read,Grep,Glob` and **no** plugin; plan/execute run
      with `--plugin-dir`; all three under a fresh empty config dir.
- [ ] `EnsureClone` returns the clone path; `worker.brainstorm` clones (after the breaker) and threads it as
      `RepoPath`; `worker.plan` discards the path.
- [ ] `config` has `claude.plugin_dir`/`claude.setting_sources` with `WAZIR_CLAUDE_*` overrides;
      `DiscoverSuperpowersPluginDir` returns the newest version.
- [ ] `wazir serve` resolves the plugin dir (config → discover → fail loud) and warns when no auth env is set.
- [ ] `internal/orchestrator` still imports no provider package (`imports_test.go` green).
- [ ] `go test ./...` green (no network/credentials); `go vet ./...` clean; `go test -race ./...` clean.
- [ ] Manual: a brainstorm turn loads the target repo's `CLAUDE.md`/`AGENTS.md`, a planted global sentinel
      stays hidden, and parallel turns get distinct config dirs.

---

## 15. Operational prerequisites (M5 isolation live run)

- `claude` installed; run `claude setup-token` once and export `CLAUDE_CODE_OAUTH_TOKEN` in the daemon's
  environment (or `ANTHROPIC_API_KEY` for the API-billed alternative).
- The Superpowers plugin installed; either set `WAZIR_CLAUDE_PLUGIN_DIR` to its absolute path or let `serve`
  auto-discover the newest version under `~/.claude/plugins/cache/claude-plugins-official/superpowers/`.
- Enough disk + `$TMPDIR` headroom for one short-lived empty config dir per in-flight turn (cleaned up
  automatically).
- Cost: brainstorm now reads the repo (more tokens per turn); the existing brainstorm timeout + max-turns
  cap bound it. The daily budget breaker is a separate M5 slice.
- The human spec-approval gate (`Spec Review → Planning`) remains the security boundary before any code runs.
