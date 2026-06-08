# Wazir M5 (slice 1) — Execution Isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run every headless `claude` turn in a hermetic, per-run config dir so concurrent turns can't corrupt each other's session state and global/orchestrator context can't bleed in — while each turn sees its *target* repo's context, and brainstorm becomes repo-aware.

**Architecture:** One load-bearing primitive — a fresh, empty `CLAUDE_CONFIG_DIR` per `claude` invocation (created, injected into the curated env, `defer`-removed). Real `HOME` is kept (toolchain caches stay warm); auth rides in via `CLAUDE_CODE_OAUTH_TOKEN`. Plan/execute add `--plugin-dir <superpowers>`; brainstorm runs with cwd = the card's repo clone + `Read,Grep,Glob`. A `--setting-sources` value (spike-pinned, configurable) stops a repo's own `.claude/settings.json` from widening our `--allowedTools` ceiling.

**Tech Stack:** Go 1.24, `os/exec`, `go.uber.org/zap`, `kkyr/fig` config, `go.etcd.io/bbolt` (unused by this slice), `spf13/cobra`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-07-wazir-m5-execution-isolation-design.md`. **Branch:** `m5-execution-isolation`.

---

## File structure (what each task touches)

| File | Responsibility | Task |
|---|---|---|
| `internal/claude/runner.go` | Per-run `CLAUDE_CONFIG_DIR`; `--plugin-dir`/`--setting-sources` flags; drop inherited `CLAUDE_CONFIG_DIR` from curated env | 2 |
| `internal/claude/runner_test.go` | Isolation invariants (relocate/remove/distinct, token+HOME, flags, cleanup-on-failure) | 2 |
| `internal/config/config.go` | `claude.plugin_dir`, `claude.setting_sources` | 3 |
| `internal/config/config_test.go` | Defaults + env overrides for the two new claude fields | 3 |
| `internal/forge/forge.go` | `EnsureClone` returns the clone path | 4 |
| `internal/forge/github/forge.go` | Return the clone dir from `EnsureClone` | 4 |
| `internal/forge/github/forge_git_test.go`, `integration_test.go`, `internal/forge/forge_test.go`, `internal/server/server_test.go`, `internal/orchestrator/worker_test.go` | Update `EnsureClone` call sites / stubs to the new signature | 4 |
| `internal/orchestrator/brain.go` | `BrainstormInput.RepoPath` | 5 |
| `internal/orchestrator/worker.go` | `brainstorm()` clones (after the cap) and threads the path; `plan()` discards the path | 5 |
| `internal/orchestrator/worker_test.go` | brainstorm uses the clone; cap skips the clone | 5 |
| `internal/claude/plugin.go` | `DiscoverSuperpowersPluginDir(home)` | 6 |
| `internal/claude/plugin_test.go` | Discovery: newest version / empty → error | 6 |
| `internal/claude/brain.go` | brain carries `pluginDir`/`settingSources`; brainstorm cwd+tools; plan/execute `--plugin-dir` | 6 |
| `internal/claude/brain_test.go` | brainstorm + plan/execute argv | 6 |
| `cmd/wazir/serve.go` | Resolve plugin dir (config → discover → fail loud); warn on missing auth env | 7 |
| `wazir.example.yaml`, `CLAUDE.md` | Document the new config + M5 isolation status | 8 |

**Dependency rule:** `internal/orchestrator` still imports only `board`/`forge`/`store` + its own `Brain` port. The clone path is a plain `string` — no provider type crosses the port; `imports_test.go` stays green.

---

## Task 1: Spike — pin the CLI recipe (RUN BY USER; metered)

The user runs this (headless `claude` is metered). It produces a short findings addendum to the spec and pins two values the code tasks read from config: the `--setting-sources` value and whether `AGENTS.md` auto-loads. The code tasks (2–8) implement Approach A and do **not** hard-block on the spike — they default `setting_sources` to empty (flag omitted) and `plugin_dir` to auto-discovery; the spike confirms `--plugin-dir` and tunes `setting_sources`.

- [ ] **Step 1: Confirm the flags exist (non-metered)**

Run:
```bash
claude --version    # record + pin this version in the spec addendum
claude --help | grep -E -- '--plugin-dir|--setting-sources|--bare'
```
Expected: `--plugin-dir` and `--setting-sources` are listed. (`--bare` presence informs the Approach-C fallback.)

- [ ] **Step 2: Confirm empty config dir + token auth (tiny metered run)**

Run:
```bash
CLAUDE_CONFIG_DIR="$(mktemp -d)" CLAUDE_CODE_OAUTH_TOKEN="$(cat ~/.wazir-claude-token 2>/dev/null || echo "$CLAUDE_CODE_OAUTH_TOKEN")" \
  claude -p "reply with the single word: ok" --output-format json
```
Expected: a clean JSON result envelope (no interactive onboarding hang), authenticated.

- [ ] **Step 3: Confirm `--plugin-dir` resolves Superpowers from a relocated dir, no global leak**

Plant a sentinel in the real user memory, then run from a throwaway clone:
```bash
echo "WAZIR-GLOBAL-SENTINEL-do-not-reveal" >> ~/.claude/CLAUDE.md   # remember to remove after
SP=$(ls -d ~/.claude/plugins/cache/claude-plugins-official/superpowers/*/ | sort -V | tail -1)
CLAUDE_CONFIG_DIR="$(mktemp -d)" claude -p "/superpowers:write-plan say the plan filename you would create, then stop" \
  --plugin-dir "$SP" --output-format json
```
Expected: the `/superpowers:write-plan` command resolves; the output does **not** mention `WAZIR-GLOBAL-SENTINEL`.

- [ ] **Step 4: Confirm the brainstorm recipe (repo CLAUDE.md/AGENTS.md load; AGENTS.md decision)**

In a scratch clone that has both a `CLAUDE.md` and an `AGENTS.md` with distinguishable marker lines:
```bash
cd /path/to/scratch-clone
CLAUDE_CONFIG_DIR="$(mktemp -d)" claude -p "List the project-specific instructions you were given, verbatim." \
  --allowedTools "Read,Grep,Glob" --setting-sources user --output-format json
```
Expected: the `CLAUDE.md` marker appears. Record whether the `AGENTS.md` marker also appears → if **not**, note in the addendum that a future tweak injects `AGENTS.md` into the transcript (out of this slice; the slice ships `CLAUDE.md` auto-load + exploration).

- [ ] **Step 5: Confirm the settings guard**

Add `.claude/settings.json` with `{"permissions":{"allow":["Bash(curl:*)"]}}` to the scratch clone, then re-run Step 4's command and ask the model to run `curl`. Try `--setting-sources` values (`user`, then `user,local`, etc.) until the planted `Bash(curl:*)` is **not** honored while the `CLAUDE.md` marker still loads. **Record the winning value** — it becomes the `claude.setting_sources` default in Task 3 / the spec addendum. If no value cleanly separates context from settings, record the residual (the base `--allowedTools` remains the ceiling) and leave `setting_sources` empty.

- [ ] **Step 6: Write the findings addendum + cleanup**

Remove the planted sentinel (`~/.claude/CLAUDE.md`) and scratch files. Append a `## Empirical findings (Task 0 spike)` section to the spec: CLI version, `--plugin-dir` go/no-go (A vs C), pinned `setting_sources` value, `AGENTS.md` decision. Commit:
```bash
git add docs/superpowers/specs/2026-06-07-wazir-m5-execution-isolation-design.md
git commit -m "docs(m5): record execution-isolation spike findings"
```

> If Step 1/Step 3 show `--plugin-dir` is unavailable, STOP and pivot to Approach C (seed a minimal config dir) — a separate plan. The remaining tasks assume the Approach-A go.

---

## Task 2: Runner — per-run `CLAUDE_CONFIG_DIR` + isolation flags

**Files:**
- Modify: `internal/claude/runner.go` (`RunSpec` struct; `Run`; `curatedEnv`)
- Test: `internal/claude/runner_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/claude/runner_test.go`:

```go
func TestRunnerRelocatesConfigDirPerRun(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}

	readConfigDir := func() string {
		if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		env, err := os.ReadFile(bin + ".env")
		if err != nil {
			t.Fatalf("read env: %v", err)
		}
		for _, line := range strings.Split(string(env), "\n") {
			if v, ok := strings.CutPrefix(line, "CLAUDE_CONFIG_DIR="); ok {
				return v
			}
		}
		t.Fatal("CLAUDE_CONFIG_DIR not set in claude child env")
		return ""
	}

	first := readConfigDir()
	tmpRoot, _ := filepath.EvalSymlinks(os.TempDir())
	if fr, _ := filepath.EvalSymlinks(first); !strings.HasPrefix(fr, tmpRoot) {
		t.Errorf("config dir %q not under temp root %q", first, tmpRoot)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Errorf("config dir %q must be removed after the run (stat err=%v)", first, err)
	}
	if second := readConfigDir(); first == second {
		t.Errorf("config dir must be distinct per run; got %q twice", first)
	}
}

func TestRunnerKeepsOAuthTokenAndRealHome(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-123")
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	env, _ := os.ReadFile(bin + ".env")
	if !strings.Contains(string(env), "CLAUDE_CODE_OAUTH_TOKEN=tok-123") {
		t.Errorf("curated env dropped the OAuth token:\n%s", env)
	}
	if !strings.Contains(string(env), "HOME="+os.Getenv("HOME")) {
		t.Errorf("curated env must preserve the real HOME=%q:\n%s", os.Getenv("HOME"), env)
	}
}

func TestRunnerPassesPluginAndSettingFlags(t *testing.T) {
	bin := writeFakeClaude(t, envelope("ok", false, "success"), 0, 0)
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{
		Prompt: "x", Timeout: 5 * time.Second,
		PluginDir: "/plugins/superpowers", SettingSources: "user",
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args, _ := os.ReadFile(bin + ".args")
	for _, want := range []string{"--plugin-dir", "/plugins/superpowers", "--setting-sources", "user"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argv missing %q; got:\n%s", want, args)
		}
	}
}

func TestRunnerRemovesConfigDirOnFailure(t *testing.T) {
	bin := writeFakeClaude(t, "", 2, 0) // non-zero exit, but env is written first
	r := &Runner{bin: bin, log: zap.NewNop()}
	if _, err := r.Run(context.Background(), RunSpec{Prompt: "x", Timeout: 5 * time.Second}); err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	env, err := os.ReadFile(bin + ".env")
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	var cfgDir string
	for _, line := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(line, "CLAUDE_CONFIG_DIR="); ok {
			cfgDir = v
		}
	}
	if cfgDir == "" {
		t.Fatal("CLAUDE_CONFIG_DIR not captured")
	}
	if _, err := os.Stat(cfgDir); !os.IsNotExist(err) {
		t.Errorf("config dir %q must be removed even after a failed run (stat err=%v)", cfgDir, err)
	}
}
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `go test ./internal/claude/ -run 'TestRunnerRelocatesConfigDirPerRun|TestRunnerKeepsOAuthTokenAndRealHome|TestRunnerPassesPluginAndSettingFlags|TestRunnerRemovesConfigDirOnFailure' -v`
Expected: compile error (`unknown field PluginDir`) or FAIL (no `CLAUDE_CONFIG_DIR`).

- [ ] **Step 3: Add the two `RunSpec` fields**

In `internal/claude/runner.go`, add to the `RunSpec` struct (after `PermissionMode`):

```go
	PermissionMode  string
	PluginDir       string // M5: --plugin-dir <path> (plan/execute load Superpowers; brainstorm leaves empty)
	SettingSources  string // M5: --setting-sources <v> (stops a repo's .claude/settings.json widening tools)
```

- [ ] **Step 4: Create + inject + remove the per-run config dir, and add the flags**

In `internal/claude/runner.go`, inside `Run`, append the flags after the existing `DisallowedTools` block:

```go
	if len(spec.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(spec.DisallowedTools, ","))
	}
	if spec.PluginDir != "" {
		args = append(args, "--plugin-dir", spec.PluginDir)
	}
	if spec.SettingSources != "" {
		args = append(args, "--setting-sources", spec.SettingSources)
	}
```

Then, where `cmd.Env = curatedEnv()` currently is, replace it with a per-run config dir:

```go
	cmd.Dir = dir
	// Per-run isolated config dir: an empty CLAUDE_CONFIG_DIR means no global
	// ~/.claude/CLAUDE.md, no globally-enabled plugins, no global MCP, and isolated
	// session state (parallel-safe). Removed when the run returns, on every path.
	cfgDir, err := os.MkdirTemp("", "wazir-cfg-")
	if err != nil {
		return RunResult{}, fmt.Errorf("create isolated config dir: %w", err)
	}
	defer os.RemoveAll(cfgDir)
	cmd.Env = append(curatedEnv(), "CLAUDE_CONFIG_DIR="+cfgDir)
```

- [ ] **Step 5: Drop any inherited `CLAUDE_CONFIG_DIR` from the curated env**

In `internal/claude/runner.go`, in `curatedEnv`, make the per-run value authoritative by never inheriting one. Change the `keep` closure to special-case it first:

```go
	keep := func(k string) bool {
		if k == "CLAUDE_CONFIG_DIR" {
			return false // set per-run by Run, never inherited
		}
		if keepExact[k] {
			return true
		}
		for _, p := range keepPrefix {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/claude/ -v`
Expected: PASS (new tests + all existing runner/brain tests).

- [ ] **Step 7: Commit**

```bash
git add internal/claude/runner.go internal/claude/runner_test.go
git commit -m "feat(claude): per-run CLAUDE_CONFIG_DIR + --plugin-dir/--setting-sources isolation"
```

---

## Task 3: Config — `claude.plugin_dir` + `claude.setting_sources`

**Files:**
- Modify: `internal/config/config.go` (`ClaudeConfig`)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestClaudeIsolationConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("WAZIR_GITHUB_PAT", "x")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "7")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Claude.PluginDir != "" || c.Claude.SettingSources != "" {
		t.Errorf("isolation fields should default empty, got plugin_dir=%q setting_sources=%q", c.Claude.PluginDir, c.Claude.SettingSources)
	}

	t.Setenv("WAZIR_CLAUDE_PLUGIN_DIR", "/opt/sp")
	t.Setenv("WAZIR_CLAUDE_SETTING_SOURCES", "user")
	c2, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c2.Claude.PluginDir != "/opt/sp" || c2.Claude.SettingSources != "user" {
		t.Errorf("env overrides not applied: plugin_dir=%q setting_sources=%q", c2.Claude.PluginDir, c2.Claude.SettingSources)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestClaudeIsolationConfig -v`
Expected: compile error (`unknown field PluginDir`).

- [ ] **Step 3: Add the two fields**

In `internal/config/config.go`, add to `ClaudeConfig` (after `ExecuteAllowedTools`):

```go
	ExecuteAllowedTools string        `fig:"execute_allowed_tools" default:"Read,Edit,Write,Bash(git:*),Bash(go:*),Bash(gofmt:*),Bash(ls:*),Bash(cat:*)"`
	PluginDir           string        `fig:"plugin_dir"`      // WAZIR_CLAUDE_PLUGIN_DIR ("" = serve auto-discovers)
	SettingSources      string        `fig:"setting_sources"` // WAZIR_CLAUDE_SETTING_SOURCES (spike-pinned; "" = flag omitted)
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): claude plugin_dir + setting_sources (M5 isolation)"
```

---

## Task 4: Forge — `EnsureClone` returns the clone path

**Files:**
- Modify: `internal/forge/forge.go` (interface), `internal/forge/github/forge.go` (impl)
- Modify (call sites/stubs): `internal/orchestrator/worker.go:181`, `internal/orchestrator/worker_test.go`, `internal/server/server_test.go:107`, `internal/forge/forge_test.go:19`, `internal/forge/github/forge_git_test.go`, `internal/forge/github/integration_test.go`

- [ ] **Step 1: Update the interface**

In `internal/forge/forge.go`, change the `EnsureClone` line and its doc:

```go
	// EnsureClone makes the local clone for repo present and current
	// (clone if absent, else fetch) and returns its absolute path. Idempotent.
	EnsureClone(ctx context.Context, repo string) (clonePath string, err error)
```

- [ ] **Step 2: Update the GitHub impl**

In `internal/forge/github/forge.go`, replace the whole `EnsureClone` method:

```go
// EnsureClone makes the clone present + current (clone if absent, else fetch) and returns its path.
func (f *GitHubForge) EnsureClone(ctx context.Context, repo string) (string, error) {
	clone, err := f.clonePath(repo)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(filepath.Join(clone, ".git")); statErr == nil {
		if err := f.resetOrigin(ctx, clone, repo); err != nil {
			return "", err
		}
		if _, err := f.git.run(ctx, clone, true, "fetch", "origin", "--prune"); err != nil {
			return "", err
		}
		return clone, nil
	}
	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		return "", fmt.Errorf("mkdir clone parent: %w", err)
	}
	if _, err := f.git.run(ctx, "", true, "clone", f.remoteURL(repo), clone); err != nil {
		return "", err
	}
	return clone, nil
}
```

- [ ] **Step 3: Update the worker caller (`plan`)**

In `internal/orchestrator/worker.go`, in `plan()`, change the `EnsureClone` call to discard the path:

```go
	if _, err := w.forge.EnsureClone(ctx, card.Repo); err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}
```

- [ ] **Step 4: Update the test stubs/fakes**

`internal/server/server_test.go:107`:
```go
func (noForge) EnsureClone(ctx context.Context, repo string) (string, error)                  { return "", nil }
```

`internal/forge/forge_test.go:19`:
```go
func (shapeStub) EnsureClone(ctx context.Context, repo string) (string, error) { return "", nil }
```

`internal/orchestrator/worker_test.go` — add a `clonePath` field to `fakeForge` and update the method:
```go
type fakeForge struct {
	prURL     string
	pushErr   error
	wtPath    string // path CreateWorktree returns ("" by default)
	clonePath string // path EnsureClone returns ("" by default)
	pushed    bool
	removed   bool
	calls     []string // ordered: ensureClone, createWorktree, push, openPR, removeWorktree
}

func (f *fakeForge) EnsureClone(ctx context.Context, repo string) (string, error) {
	f.calls = append(f.calls, "ensureClone")
	return f.clonePath, nil
}
```

`internal/forge/github/forge_git_test.go` — at all four call sites, ignore the returned path:
```go
	if _, err := f.EnsureClone(ctx, repo); err != nil {
```

`internal/forge/github/integration_test.go:31`:
```go
	if _, err := f.EnsureClone(ctx, repo); err != nil {
```

- [ ] **Step 5: Verify the tree compiles + tests pass**

Run: `go build ./... && go test ./internal/forge/... ./internal/orchestrator/... ./internal/server/...`
Expected: build OK; PASS. (The integration test is build-tagged and skipped — confirm it still compiles with `go vet -tags integration ./internal/forge/github/`.)

- [ ] **Step 6: Commit**

```bash
git add internal/forge/forge.go internal/forge/github/ internal/orchestrator/worker.go internal/orchestrator/worker_test.go internal/server/server_test.go internal/forge/forge_test.go
git commit -m "refactor(forge): EnsureClone returns the clone path (for repo-aware brainstorm)"
```

---

## Task 5: Orchestrator — repo-aware brainstorm threads the clone path

**Files:**
- Modify: `internal/orchestrator/brain.go` (`BrainstormInput`), `internal/orchestrator/worker.go` (`brainstorm`)
- Test: `internal/orchestrator/worker_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/orchestrator/worker_test.go`. First extend `scriptedBrain` to record the RepoPath — change its `Brainstorm` method and add a field:

```go
type scriptedBrain struct {
	brainstorm []BrainstormResult
	plan       []PlanResult
	execute    []ExecuteResult
	err        error

	brainstormCalls        int
	lastExecPlanPath       string
	lastBrainstormRepoPath string // records RepoPath the last Brainstorm received
}

func (s *scriptedBrain) Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error) {
	s.brainstormCalls++
	s.lastBrainstormRepoPath = in.RepoPath
	if s.err != nil {
		return BrainstormResult{}, s.err
	}
	r := s.brainstorm[0]
	s.brainstorm = s.brainstorm[1:]
	return r, nil
}
```

Then add the tests:

```go
func TestWorkerBrainstormUsesRepoClone(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q?"}}}}
	ff := &fakeForge{clonePath: "/clone/o-r"}
	w := NewWorker(b, ff, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !slices.Contains(ff.calls, "ensureClone") {
		t.Errorf("brainstorm must EnsureClone the repo; calls=%v", ff.calls)
	}
	if brain.lastBrainstormRepoPath != "/clone/o-r" {
		t.Errorf("brainstorm RepoPath = %q, want /clone/o-r (the clone, as cwd)", brain.lastBrainstormRepoPath)
	}
}

func TestWorkerBrainstormCapSkipsClone(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", BrainstormTurns: 2})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q?"}}}}
	ff := &fakeForge{clonePath: "/clone/o-r"}
	w := NewWorker(b, ff, brain, st, nil).WithMaxBrainstormTurns(2)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if slices.Contains(ff.calls, "ensureClone") {
		t.Errorf("the question-cap escalation must NOT clone (no work past the cap); calls=%v", ff.calls)
	}
	if brain.brainstormCalls != 0 {
		t.Errorf("brain called %d times past the cap, want 0", brain.brainstormCalls)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/orchestrator/ -run 'TestWorkerBrainstormUsesRepoClone|TestWorkerBrainstormCapSkipsClone' -v`
Expected: compile error (`unknown field RepoPath`).

- [ ] **Step 3: Add `RepoPath` to `BrainstormInput`**

In `internal/orchestrator/brain.go`:

```go
type BrainstormInput struct {
	Transcript string
	RepoPath   string // M5: the card's repo clone; used as the claude cmd.Dir so the target repo's CLAUDE.md/AGENTS.md load
}
```

- [ ] **Step 4: Clone + thread the path in `worker.brainstorm`**

In `internal/orchestrator/worker.go`, in `brainstorm()`, replace the `brain.Brainstorm` call (the lines right after the max-turns escalation block) with an `EnsureClone` first:

```go
	// Repo-aware brainstorm (M5): clone the target repo so the turn runs with cwd =
	// the clone and loads the repo's own CLAUDE.md/AGENTS.md. Done *after* the cap
	// check so an about-to-escalate card never triggers a clone.
	clonePath, err := w.forge.EnsureClone(ctx, card.Repo)
	if err != nil {
		return fmt.Errorf("ensure clone: %w", err)
	}
	res, err := w.brain.Brainstorm(ctx, BrainstormInput{Transcript: BuildTranscript(card), RepoPath: clonePath})
	if err != nil {
		return fmt.Errorf("brainstorm: %w", err)
	}
```

(The existing `rec` read at the top of `brainstorm` and the cap block stay; this replaces only the old `res, err := w.brain.Brainstorm(ctx, BrainstormInput{Transcript: BuildTranscript(card)})` line and shadows the outer `err`.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/orchestrator/ -v`
Expected: PASS (new tests + all existing worker tests — the existing brainstorm tests now also call `EnsureClone`, which the fakeForge records harmlessly).

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/brain.go internal/orchestrator/worker.go internal/orchestrator/worker_test.go
git commit -m "feat(orchestrator): repo-aware brainstorm — clone (after the cap) and thread the path"
```

---

## Task 6: Claude brain — plugin discovery + per-phase isolation wiring

**Files:**
- Create: `internal/claude/plugin.go`
- Test: `internal/claude/plugin_test.go`
- Modify: `internal/claude/brain.go`
- Test: `internal/claude/brain_test.go`

- [ ] **Step 1: Write the failing discovery test**

Create `internal/claude/plugin_test.go`:

```go
package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSuperpowersPluginDir(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "superpowers")
	for _, v := range []string{"5.1.0", "5.10.0", "5.2.0"} {
		if err := os.MkdirAll(filepath.Join(base, v), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", v, err)
		}
	}
	got, err := DiscoverSuperpowersPluginDir(home)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if filepath.Base(got) != "5.10.0" {
		t.Errorf("got %q, want the newest version 5.10.0", got)
	}
}

func TestDiscoverSuperpowersPluginDirMissing(t *testing.T) {
	if _, err := DiscoverSuperpowersPluginDir(t.TempDir()); err == nil {
		t.Fatal("expected an error when no superpowers cache exists")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/claude/ -run TestDiscoverSuperpowersPluginDir -v`
Expected: compile error (`undefined: DiscoverSuperpowersPluginDir`).

- [ ] **Step 3: Implement discovery**

Create `internal/claude/plugin.go`:

```go
package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DiscoverSuperpowersPluginDir returns the newest installed Superpowers plugin
// directory under <home>/.claude/plugins/cache/claude-plugins-official/superpowers/<version>/,
// or an error if none is found. Used when claude.plugin_dir is unset. It reads
// the real ~/.claude — the per-run config-dir relocation happens later, in Run.
func DiscoverSuperpowersPluginDir(home string) (string, error) {
	base := filepath.Join(home, ".claude", "plugins", "cache", "claude-plugins-official", "superpowers")
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", fmt.Errorf("read superpowers cache %s: %w", base, err)
	}
	best := ""
	var bestV [3]int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := parseSemver(e.Name())
		if best == "" || lessSemver(bestV, v) {
			best, bestV = e.Name(), v
		}
	}
	if best == "" {
		return "", fmt.Errorf("no superpowers plugin version found under %s", base)
	}
	return filepath.Join(base, best), nil
}

// parseSemver turns "5.10.0" into [5,10,0]; non-numeric parts become 0.
func parseSemver(s string) [3]int {
	var v [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		n, _ := strconv.Atoi(part)
		v[i] = n
	}
	return v
}

func lessSemver(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
```

- [ ] **Step 4: Run to verify discovery passes**

Run: `go test ./internal/claude/ -run TestDiscoverSuperpowersPluginDir -v`
Expected: PASS (both cases).

- [ ] **Step 5: Write the failing brain argv tests**

Add to `internal/claude/brain_test.go`:

```go
func TestBrainstormRunsInRepoCloneReadOnly(t *testing.T) {
	result := "```json\n{\"phase\":\"brainstorm\",\"status\":\"needs_answers\",\"questions\":[\"q?\"],\"spec_markdown\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(result, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, Timeout: 5 * time.Second, PluginDir: "/sp", SettingSources: "user"}, zap.NewNop())
	clone := t.TempDir()
	if _, err := br.Brainstorm(context.Background(), orchestrator.BrainstormInput{Transcript: "x", RepoPath: clone}); err != nil {
		t.Fatalf("Brainstorm: %v", err)
	}
	pwd, _ := os.ReadFile(bin + ".pwd")
	gotReal, _ := filepath.EvalSymlinks(strings.TrimSpace(string(pwd)))
	cloneReal, _ := filepath.EvalSymlinks(clone)
	if gotReal != cloneReal {
		t.Errorf("brainstorm cmd.Dir = %q, want the repo clone %q", strings.TrimSpace(string(pwd)), clone)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "Read,Grep,Glob") {
		t.Errorf("brainstorm must allow read-only exploration; args:\n%s", args)
	}
	if !strings.Contains(string(args), "--setting-sources") || !strings.Contains(string(args), "user") {
		t.Errorf("brainstorm must carry --setting-sources; args:\n%s", args)
	}
	if strings.Contains(string(args), "--plugin-dir") {
		t.Errorf("brainstorm must NOT load a plugin; args:\n%s", args)
	}
}

func TestPlanAndExecuteLoadPlugin(t *testing.T) {
	planJSON := "```json\n{\"phase\":\"plan\",\"status\":\"plan_ready\",\"plan_path\":\"docs/plan.md\",\"summary\":\"s\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(planJSON, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{
		Bin: bin, PlanTimeout: 5 * time.Second, ExecuteTimeout: 5 * time.Second,
		ExecuteAllowedTools: "Read,Edit", PluginDir: "/sp", SettingSources: "user",
	}, zap.NewNop())
	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "--plugin-dir") || !strings.Contains(string(args), "/sp") {
		t.Errorf("plan must carry --plugin-dir; args:\n%s", args)
	}
}
```

- [ ] **Step 6: Run to verify the brain tests fail**

Run: `go test ./internal/claude/ -run 'TestBrainstormRunsInRepoCloneReadOnly|TestPlanAndExecuteLoadPlugin' -v`
Expected: FAIL (no `--plugin-dir`/`Read,Grep,Glob` in argv; wrong cwd).

- [ ] **Step 7: Wire the brain**

In `internal/claude/brain.go`:

(a) Add a brainstorm allowlist near `brainstormDisallowedTools`:
```go
// brainstormAllowedTools lets the repo-aware brainstorm turn read the target repo
// (cwd = the clone) without giving it any write/exec/network capability.
var brainstormAllowedTools = []string{"Read", "Grep", "Glob"}
```

(b) Add fields to `ClaudeBrain`:
```go
type ClaudeBrain struct {
	runner              *Runner
	model               string
	timeout             time.Duration
	planTimeout         time.Duration
	executeTimeout      time.Duration
	executeAllowedTools []string
	pluginDir           string
	settingSources      string
	log                 *zap.Logger
}
```

(c) Set them in `New`:
```go
	return &ClaudeBrain{
		runner:              &Runner{bin: cfg.Bin, log: log},
		model:               cfg.Model,
		timeout:             cfg.Timeout,
		planTimeout:         cfg.PlanTimeout,
		executeTimeout:      cfg.ExecuteTimeout,
		executeAllowedTools: splitTools(cfg.ExecuteAllowedTools),
		pluginDir:           cfg.PluginDir,
		settingSources:      cfg.SettingSources,
		log:                 log,
	}
```

(d) Update the `Brainstorm` `RunSpec` (add `Dir`, `AllowedTools`, `SettingSources`):
```go
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:          in.Transcript,
		SystemPrompt:    brainstormSystemPrompt,
		Dir:             in.RepoPath,
		Model:           c.model,
		Timeout:         c.timeout,
		PermissionMode:  "default",
		AllowedTools:    brainstormAllowedTools,
		DisallowedTools: brainstormDisallowedTools,
		SettingSources:  c.settingSources,
	})
```

(e) Update the `Plan` `RunSpec` (add `PluginDir`, `SettingSources`):
```go
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:         "/superpowers:write-plan Write an implementation plan for this approved spec in the current repository.\n\nApproved spec:\n\n" + in.Spec + "\n\nIssue context:\n\n" + in.Transcript,
		SystemPrompt:   planSystemPrompt,
		Dir:            in.WorktreePath,
		Model:          c.model,
		Timeout:        c.planTimeout,
		AllowedTools:   planAllowedTools,
		PermissionMode: "acceptEdits",
		PluginDir:      c.pluginDir,
		SettingSources: c.settingSources,
	})
```

(f) Update the `Execute` `RunSpec` (add `PluginDir`, `SettingSources`):
```go
	res, err := c.runner.Run(ctx, RunSpec{
		Prompt:         "/superpowers:execute-plan Execute the implementation plan at " + in.PlanPath + ". Commit your work on the current branch; do not push or open a PR.\n\nIssue context:\n\n" + in.Transcript,
		SystemPrompt:   executeSystemPrompt,
		Dir:            in.WorktreePath,
		Model:          c.model,
		Timeout:        c.executeTimeout,
		AllowedTools:   c.executeAllowedTools,
		PermissionMode: "acceptEdits",
		PluginDir:      c.pluginDir,
		SettingSources: c.settingSources,
	})
```

- [ ] **Step 8: Run the full claude package tests**

Run: `go test ./internal/claude/ -v`
Expected: PASS (new + existing; the existing `TestPlanReady`/`TestExecuteComplete` build the brain without `PluginDir`, so they still see no `--plugin-dir` and pass).

- [ ] **Step 9: Commit**

```bash
git add internal/claude/plugin.go internal/claude/plugin_test.go internal/claude/brain.go internal/claude/brain_test.go
git commit -m "feat(claude): plugin discovery + per-phase isolation (brainstorm cwd/tools, plan/execute --plugin-dir)"
```

---

## Task 7: serve wiring — resolve the plugin dir; warn on missing auth

**Files:**
- Modify: `cmd/wazir/serve.go`

(No unit test — `serve` is daemon wiring, like M4; covered by `go build ./...` + manual run.)

- [ ] **Step 1: Add the `os` import**

In `cmd/wazir/serve.go`, add `"os"` to the stdlib import group (alongside `"os/signal"`).

- [ ] **Step 2: Resolve the plugin dir + warn on missing auth before building the brain**

In `cmd/wazir/serve.go`, replace the line `brain := claude.New(cfg.Claude, logger)` with:

```go
	// Resolve the Superpowers plugin dir plan/execute load via --plugin-dir under
	// the per-run isolated config dir. Fail loudly at startup, not mid-turn.
	if cfg.Claude.PluginDir == "" {
		home, _ := os.UserHomeDir()
		pluginDir, derr := claude.DiscoverSuperpowersPluginDir(home)
		if derr != nil {
			return fmt.Errorf("locate superpowers plugin (set WAZIR_CLAUDE_PLUGIN_DIR): %w", derr)
		}
		cfg.Claude.PluginDir = pluginDir
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		logger.Warn("no claude auth env set (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY); " +
			"headless runs will fail to authenticate under the isolated config dir")
	}
	brain := claude.New(cfg.Claude, logger)
```

- [ ] **Step 3: Verify the build**

Run: `go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add cmd/wazir/serve.go
git commit -m "feat(serve): resolve superpowers plugin dir; warn when no claude auth env is set"
```

---

## Task 8: Docs — config template + CLAUDE.md status

**Files:**
- Modify: `wazir.example.yaml`, `CLAUDE.md`

- [ ] **Step 1: Document the new claude config**

In `wazir.example.yaml`, replace the trailing `# (M4 additions ...)` comment block (lines starting `# (M4 additions to the claude section`) with one that also covers M5:

```yaml
# (M4/M5 additions to the claude section — merge into the existing claude block.)
# claude:
#   plan_timeout: 10m
#   execute_timeout: 30m   # execute needs longer than brainstorm
#   execute_allowed_tools: "Read,Edit,Write,Bash(git:*),Bash(go:*),Bash(gofmt:*),Bash(ls:*),Bash(cat:*)"
#   plugin_dir: ""         # M5: abs path to the Superpowers plugin; "" = auto-discover newest under ~/.claude
#   setting_sources: ""    # M5: value for --setting-sources (spike-pinned); "" = omit the flag
#
# M5 isolation: each claude turn runs under a fresh, empty CLAUDE_CONFIG_DIR (no
# global ~/.claude/CLAUDE.md, no other plugins). Authenticate the daemon with a
# long-lived token instead of Keychain:  export CLAUDE_CODE_OAUTH_TOKEN=...
```

- [ ] **Step 2: Update CLAUDE.md status + config notes**

In `CLAUDE.md`, update the `## Status` paragraph to note M5 slice 1 is in progress (execution isolation: per-run `CLAUDE_CONFIG_DIR`, `--plugin-dir`, token auth, repo-aware brainstorm), referencing `docs/superpowers/specs/2026-06-07-wazir-m5-execution-isolation-design.md` and `docs/superpowers/plans/2026-06-07-wazir-m5-execution-isolation.md`. In the "Configuration (fig)" section, add that `claude.plugin_dir`/`claude.setting_sources` exist and that `CLAUDE_CODE_OAUTH_TOKEN` is the daemon's claude auth.

- [ ] **Step 3: Verify nothing broke**

Run: `go build ./...`
Expected: clean (docs-only, but confirms no accidental edits).

- [ ] **Step 4: Commit**

```bash
git add wazir.example.yaml CLAUDE.md
git commit -m "docs(m5): document execution-isolation config + status"
```

---

## Task 9: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Run the whole suite, vet, and the race detector**

Run:
```bash
go build ./...
go test ./...
go vet ./...
go test -race ./...
```
Expected: all green, no network/credentials needed. (`-race` exercises the distinct-per-run config-dir test.)

- [ ] **Step 2: Confirm the dependency rule still holds**

Run: `go test ./internal/orchestrator/ -run TestImports -v` (or whatever `imports_test.go` names its test)
Expected: PASS — the core imports no provider package.

- [ ] **Step 3: Confirm the integration test still compiles**

Run: `go vet -tags integration ./internal/forge/github/`
Expected: clean (the build-tagged live test compiles against the new `EnsureClone` signature).

- [ ] **Step 4: Fold in the spike's pinned values (if Task 1 is done)**

If the Task 1 spike pinned a `setting_sources` value, set it as the documented recommendation in `wazir.example.yaml` / `CLAUDE.md` (the config field default stays empty so absence ⇒ omit-flag). Confirm the `AGENTS.md` decision is recorded in the spec addendum. Commit any doc tweak:
```bash
git add -A && git commit -m "docs(m5): pin setting_sources + AGENTS.md decision from spike"
```

---

## Self-review notes (author)

- **Spec coverage:** runner isolation (§3, §5) → Task 2; config (§9) → Task 3; `EnsureClone` path (§1, §4) → Task 4; repo-aware brainstorm (§3.1, §6, §8) → Tasks 5–6; plugin discovery + serve (§6, §10) → Tasks 6–7; spike (§7) → Task 1; docs → Task 8; testing (§12) → woven through + Task 9. The `--setting-sources` value and `AGENTS.md` auto-load are deliberately spike-resolved (Task 1) with safe defaults so no task stalls.
- **Type consistency:** `RunSpec.PluginDir`/`SettingSources`, `BrainstormInput.RepoPath`, `fakeForge.clonePath`, `scriptedBrain.lastBrainstormRepoPath`, `DiscoverSuperpowersPluginDir(home string) (string, error)`, and the new `EnsureClone(...) (string, error)` signature are used identically across tasks.
- **Out of scope (per spec §13):** container/egress (M6), per-repo clone locking (resilience slice), multi-plugin sets (M6).
