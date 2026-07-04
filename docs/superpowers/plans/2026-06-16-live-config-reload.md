# Live Config Reload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a running `wazir serve` pick up `wazir.yaml` edits to the safe subset (`claude.*` minus `bin`, `repos`, `bot_login`, `webhook_secret`) without a restart.

**Architecture:** A `fsnotify` watcher (`config.Watch`) re-loads the file on change and pushes the new values into three components — `ClaudeBrain`, `GitHubBoard`, `Worker` — each holding its reloadable state in an `atomic.Pointer`/`atomic.Int64` (lock-free, no torn reads). Restart-only fields are ignored on reload, with a warning.

**Tech Stack:** Go 1.24, `sync/atomic`, `github.com/fsnotify/fsnotify` (new), `kkyr/fig`, `go.uber.org/zap`, `spf13/cobra`.

**Spec:** `docs/superpowers/specs/2026-06-16-live-config-reload-design.md`. **Branch:** `live-config-reload` (off `main`).

---

## File structure (what each task touches)

| File | Responsibility | Task |
|---|---|---|
| `go.mod` / `go.sum` | add `fsnotify` | 1 |
| `internal/claude/brain.go` | `claudeSettings` + `atomic.Pointer`; `Reload`; turns read a snapshot | 2 |
| `internal/claude/brain_test.go` | reload swaps model/timeout; `-race` | 2 |
| `internal/board/github/board.go` | `boardReloadable` + `atomic.Pointer`; `snap()`/`Reload`; read sites | 3 |
| `internal/board/github/new.go` | `New` seeds the snapshot via `Reload` | 3 |
| `internal/board/github/parse_event.go` | `repoAllowed`/`ParseEvent` read the snapshot | 3 |
| `internal/board/github/{allowlist,parse_event}_test.go` | construct via `Reload`; reload test | 3 |
| `internal/orchestrator/worker.go` | `maxBrainstormTurns atomic.Int64`; `SetMaxBrainstormTurns` | 4 |
| `internal/orchestrator/worker_test.go` | cap change mid-life | 4 |
| `internal/config/watch.go` | `ResolvePath` + `Watch` (fsnotify) | 5 |
| `internal/config/watch_test.go` | `ResolvePath` cases + `Watch` reload/error | 5 |
| `cmd/wazir/serve.go` | start the watcher; reload callback; restart-only warning | 6 |
| `wazir.example.yaml`, `CLAUDE.md` | document live reload | 7 |

**Compile-stable order:** Tasks 2–5 are independent component changes (each builds + tests green on its own); Task 6 wires them together. Each task ends green.

**Merge note (not a task):** `serve.go` (Task 6) and `worker.go` (Task 4) overlap regions with the open App-auth (#7) and rework (#11) PRs; resolve on merge. `claude`/`config`/`board` changes here don't overlap them.

---

## Task 1: Add the fsnotify dependency

**Files:** `go.mod`, `go.sum`

- [ ] **Step 1: Add the module**

Run:
```bash
go get github.com/fsnotify/fsnotify@latest
go mod tidy
```
Expected: `go.mod` gains `github.com/fsnotify/fsnotify`.

- [ ] **Step 2: Verify the build**

Run: `go build ./...`
Expected: clean (nothing imports it yet).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add fsnotify for live config reload"
```

---

## Task 2: `ClaudeBrain` — atomic settings + `Reload`

**Files:**
- Modify: `internal/claude/brain.go`
- Test: `internal/claude/brain_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/claude/brain_test.go` (it already has `writeFakeClaude`, `envelope`, and imports `config`, `orchestrator`, `os`, `strings`, `time`, `zap`; add `sync`):

```go
func TestBrainReloadSwapsModel(t *testing.T) {
	planJSON := "```json\n{\"phase\":\"plan\",\"status\":\"plan_ready\",\"plan_path\":\"p.md\",\"summary\":\"s\",\"error\":\"\"}\n```"
	bin := writeFakeClaude(t, envelope(planJSON, false, "success"), 0, 0)
	br := New(config.ClaudeConfig{Bin: bin, Model: "model-a", PlanTimeout: 5 * time.Second}, zap.NewNop())

	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if args, _ := os.ReadFile(bin + ".args"); !strings.Contains(string(args), "model-a") {
		t.Fatalf("first turn argv missing model-a:\n%s", args)
	}

	br.Reload(config.ClaudeConfig{Bin: bin, Model: "model-b", PlanTimeout: 5 * time.Second})
	if _, err := br.Plan(context.Background(), orchestrator.PlanInput{Spec: "s", WorktreePath: t.TempDir()}); err != nil {
		t.Fatalf("Plan after reload: %v", err)
	}
	args, _ := os.ReadFile(bin + ".args")
	if !strings.Contains(string(args), "model-b") {
		t.Errorf("after Reload, argv must carry model-b; got:\n%s", args)
	}
}

func TestBrainReloadRace(t *testing.T) {
	br := New(config.ClaudeConfig{Bin: "true", Model: "a"}, zap.NewNop())
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); for j := 0; j < 1000; j++ { _ = br.settings.Load().model } }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); for j := 0; j < 1000; j++ { br.Reload(config.ClaudeConfig{Model: "b"}) } }()
	wg.Wait()
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/claude/ -run 'TestBrainReload' -v`
Expected: compile error (`br.settings` undefined, `br.Reload` undefined).

- [ ] **Step 3: Refactor the brain to an atomic snapshot**

In `internal/claude/brain.go`: add `"sync/atomic"` to imports. Replace the `ClaudeBrain` struct + `New` with:

```go
// claudeSettings is the hot-reloadable subset of the claude config, swapped
// atomically so a reload is observed by the next turn without a lock.
type claudeSettings struct {
	model               string
	timeout             time.Duration // brainstorm
	planTimeout         time.Duration
	executeTimeout      time.Duration
	executeAllowedTools []string
	pluginsDir          string
	pluginID            string
	settingSources      string
}

func settingsFrom(cfg config.ClaudeConfig) *claudeSettings {
	return &claudeSettings{
		model:               cfg.Model,
		timeout:             cfg.Timeout,
		planTimeout:         cfg.PlanTimeout,
		executeTimeout:      cfg.ExecuteTimeout,
		executeAllowedTools: splitTools(cfg.ExecuteAllowedTools),
		pluginsDir:          cfg.PluginsDir,
		pluginID:            cfg.PluginID,
		settingSources:      cfg.SettingSources,
	}
}

// ClaudeBrain implements orchestrator.Brain via the headless claude CLI.
type ClaudeBrain struct {
	runner   *Runner // bin is fixed (restart-only)
	settings atomic.Pointer[claudeSettings]
	log      *zap.Logger
}

// New builds a ClaudeBrain from config. A nil logger becomes a no-op.
func New(cfg config.ClaudeConfig, log *zap.Logger) *ClaudeBrain {
	if log == nil {
		log = zap.NewNop()
	}
	b := &ClaudeBrain{runner: &Runner{bin: cfg.Bin, log: log}, log: log}
	b.settings.Store(settingsFrom(cfg))
	return b
}

// Reload swaps the hot-reloadable claude settings (model, timeouts, allowed tools,
// plugin dir/id, setting sources). The binary path is fixed and not reloaded.
func (c *ClaudeBrain) Reload(cfg config.ClaudeConfig) { c.settings.Store(settingsFrom(cfg)) }
```

- [ ] **Step 4: Read the snapshot in the three turns**

In `Brainstorm`, add `s := c.settings.Load()` as the first line and change the `RunSpec` fields: `Model: s.model`, `Timeout: s.timeout`, `SettingSources: s.settingSources`.

In `Plan`, add `s := c.settings.Load()` first and change: `Model: s.model`, `Timeout: s.planTimeout`, `PluginsDir: s.pluginsDir`, `EnabledPlugin: s.pluginID`, `SettingSources: s.settingSources`.

In `Execute`, add `s := c.settings.Load()` first and change: `Model: s.model`, `Timeout: s.executeTimeout`, `AllowedTools: s.executeAllowedTools`, `PluginsDir: s.pluginsDir`, `EnabledPlugin: s.pluginID`, `SettingSources: s.settingSources`.

(The `c.model`/`c.timeout`/`c.planTimeout`/`c.executeTimeout`/`c.executeAllowedTools`/`c.pluginsDir`/`c.pluginID`/`c.settingSources` fields no longer exist; every reference becomes `s.<field>`.)

- [ ] **Step 5: Run the package tests**

Run: `go test ./internal/claude/ -v`
Expected: PASS (new tests + existing brain/runner tests; existing tests build the brain via `New`, so the internal refactor is transparent). Then `go test -race ./internal/claude/ -run TestBrainReloadRace`.

- [ ] **Step 6: Commit**

```bash
git add internal/claude/brain.go internal/claude/brain_test.go
git commit -m "feat(claude): hot-reloadable brain settings via atomic.Pointer"
```

---

## Task 3: `GitHubBoard` — atomic reloadable fields + `Reload`

**Files:**
- Modify: `internal/board/github/board.go`, `internal/board/github/new.go`, `internal/board/github/parse_event.go`
- Test: `internal/board/github/allowlist_test.go`, `internal/board/github/parse_event_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/board/github/parse_event_test.go` (imports already include `board`, `store`):

```go
func TestBoardReloadSwapsAllowListAndSecret(t *testing.T) {
	b := newParser() // repos=["octocat/hello"], botLogin="wazir-bot", webhookSecret="shh"
	if !b.repoAllowed("octocat/hello") || b.repoAllowed("octocat/other") {
		t.Fatal("precondition: initial allow-list")
	}
	b.Reload([]string{"octocat/other"}, "new-bot", "newsecret")
	if b.repoAllowed("octocat/hello") || !b.repoAllowed("octocat/other") {
		t.Errorf("allow-list not swapped by Reload")
	}
	// New webhook secret takes effect: a payload signed with the OLD secret now fails.
	payload := loadFixture(t, "issues_opened.json")
	h := headersFor("issues", "dR", sign([]byte("shh"), payload)) // old secret
	if _, err := b.ParseEvent(h, payload); err == nil {
		t.Errorf("expected signature failure under the reloaded secret")
	}
	h2 := headersFor("issues", "dR2", sign([]byte("newsecret"), payload))
	if _, err := b.ParseEvent(h2, payload); err != nil {
		t.Errorf("payload signed with the new secret should validate: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/board/github/ -run TestBoardReloadSwapsAllowListAndSecret -v`
Expected: compile error (`b.Reload` undefined).

- [ ] **Step 3: Add the reloadable snapshot to the struct**

In `internal/board/github/board.go`: add `"slices"` and `"sync/atomic"` to imports. In the `GitHubBoard` struct, **remove** the `botLogin string`, `webhookSecret string`, and `repos []string` fields and **add**:

```go
	reloadable atomic.Pointer[boardReloadable] // repos/bot_login/webhook_secret — hot-reloadable
```

Add (near the struct):

```go
// boardReloadable is the hot-reloadable subset of the board config.
type boardReloadable struct {
	repos         []string
	botLogin      string
	webhookSecret string
}

// snap returns the current reloadable settings, never nil.
func (b *GitHubBoard) snap() *boardReloadable {
	if r := b.reloadable.Load(); r != nil {
		return r
	}
	return &boardReloadable{}
}

// Reload swaps the hot-reloadable subset (allow-list, bot login, webhook secret).
func (b *GitHubBoard) Reload(repos []string, botLogin, webhookSecret string) {
	b.reloadable.Store(&boardReloadable{repos: slices.Clone(repos), botLogin: botLogin, webhookSecret: webhookSecret})
}
```

- [ ] **Step 4: Update `New` to seed the snapshot**

In `internal/board/github/new.go`, replace the `botLogin`/`repos`/`webhookSecret` literal fields with a `Reload` call:

```go
func New(httpClient *http.Client, cfg config.Config, st store.Store) *GitHubBoard {
	b := &GitHubBoard{
		api:           &ghProjects{gql: githubv4.NewClient(httpClient)},
		rest:          github.NewClient(httpClient),
		store:         st,
		owner:         cfg.Project.Owner,
		ownerType:     cfg.GitHub.OwnerType,
		projectNumber: cfg.Project.Number,
		boardName:     cfg.Project.BoardName,
	}
	b.Reload(cfg.Repos, cfg.BotLogin, cfg.GitHub.WebhookSecret)
	return b
}
```

- [ ] **Step 5: Update the read sites**

In `internal/board/github/parse_event.go`:

`repoAllowed`:
```go
func (b *GitHubBoard) repoAllowed(full string) bool {
	repos := b.snap().repos
	if len(repos) == 0 {
		return true // no allow-list configured = allow all
	}
	for _, r := range repos {
		if r == full {
			return true
		}
	}
	return false
}
```

In `ParseEvent`, load once at the top of the function (just after computing `eventType`/`delivery`, before the signature check) and use it for the secret + bot login:
```go
	rl := b.snap()
	if err := github.ValidateSignature(sig, payload, []byte(rl.webhookSecret)); err != nil {
```
Then change the two bot-login reads in `ParseEvent` to `rl.botLogin`:
- issue-comment `IsBot`: `IsBot: author == rl.botLogin || strings.Contains(body, botMarker),`
- projects_v2_item self-move filter: `if rl.botLogin != "" && e.GetSender().GetLogin() == rl.botLogin {`

In `internal/board/github/board.go`, the `GetCard` comment build (currently `author == b.botLogin`):
```go
			IsBot:   author == b.snap().botLogin || strings.Contains(body, botMarker),
```

- [ ] **Step 6: Fix the test construction sites**

`internal/board/github/parse_event_test.go` — `newParser`:
```go
func newParser() *GitHubBoard {
	b := &GitHubBoard{projectNodeID: "PROJECT_NODE_1"}
	b.Reload([]string{"octocat/hello"}, "wazir-bot", "shh")
	return b
}
```
And `TestParseDropsForeignRepo` (was `b.repos = []string{"octocat/other"}`):
```go
	b.Reload([]string{"octocat/other"}, "wazir-bot", "shh")
```

`internal/board/github/allowlist_test.go`:
- `TestResolveCardRejectsForeignRepo` (was `b := &GitHubBoard{api: api, store: st, repos: []string{"octocat/allowed"}}`):
  ```go
	b := &GitHubBoard{api: api, store: st}
	b.Reload([]string{"octocat/allowed"}, "", "")
  ```
- `TestResolveCardReResolvesStaleCachedRepo` (was `... repos: []string{"new-org/repo"}}`):
  ```go
	b := &GitHubBoard{api: api, store: st}
	b.Reload([]string{"new-org/repo"}, "", "")
  ```
- `TestParseIssueCommentDetectsBotByLogin` (was `b.botLogin = "alice"`):
  ```go
	b.Reload([]string{"octocat/hello"}, "alice", "shh")
  ```

- [ ] **Step 7: Run the package tests (incl. -race)**

Run: `go test ./internal/board/github/ -v` then `go test -race ./internal/board/github/ -run 'TestParse|TestBoardReload|TestResolveCard'`
Expected: PASS. `grep -rn "b\.repos\|b\.botLogin\|b\.webhookSecret" internal/board/github/` returns nothing (all reads go through `snap()` now).

- [ ] **Step 8: Commit**

```bash
git add internal/board/github/board.go internal/board/github/new.go internal/board/github/parse_event.go internal/board/github/allowlist_test.go internal/board/github/parse_event_test.go
git commit -m "feat(board): hot-reloadable repos/bot_login/webhook_secret via atomic.Pointer"
```

---

## Task 4: `Worker` — atomic `maxBrainstormTurns` + `SetMaxBrainstormTurns`

**Files:**
- Modify: `internal/orchestrator/worker.go`
- Test: `internal/orchestrator/worker_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/orchestrator/worker_test.go`:

```go
func TestWorkerSetMaxBrainstormTurnsLive(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", BrainstormTurns: 2})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q?"}}}}
	ff := &fakeForge{clonePath: "/clone/o-r"}
	w := NewWorker(b, ff, brain, st, nil) // default cap 8 → 2 turns is under it

	// Lower the cap live to 2; the card (2 turns) is now at the cap → escalate, no brain call, no clone.
	w.SetMaxBrainstormTurns(2)
	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if brain.brainstormCalls != 0 {
		t.Errorf("brain ran %d times; the live-lowered cap should escalate without a turn", brain.brainstormCalls)
	}
	if slices.Contains(ff.calls, "ensureClone") {
		t.Errorf("escalation must not clone; calls=%v", ff.calls)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/orchestrator/ -run TestWorkerSetMaxBrainstormTurnsLive -v`
Expected: compile error (`w.SetMaxBrainstormTurns` undefined).

- [ ] **Step 3: Make the cap atomic**

In `internal/orchestrator/worker.go`: add `"sync/atomic"` to imports. Change the struct field:
```go
	maxBrainstormTurns atomic.Int64 // cap on the clarifying-question loop (M2); hot-reloadable
```
Replace `NewWorker`'s return (the literal can't set an atomic to a non-zero value, so store after building):
```go
func NewWorker(b board.Board, f forge.CodeForge, br Brain, st store.Store, log *zap.Logger) *Worker {
	if log == nil {
		log = zap.NewNop()
	}
	w := &Worker{board: b, forge: f, brain: br, store: st, log: log, base: "main"}
	w.maxBrainstormTurns.Store(defaultMaxBrainstormTurns)
	return w
}
```
Replace `WithMaxBrainstormTurns` and add `SetMaxBrainstormTurns`:
```go
// WithMaxBrainstormTurns overrides the question-loop cap (e.g. from config).
// A non-positive n is ignored. Returns w for chaining.
func (w *Worker) WithMaxBrainstormTurns(n int) *Worker {
	w.SetMaxBrainstormTurns(n)
	return w
}

// SetMaxBrainstormTurns hot-swaps the question-loop cap (config reload). Ignores n <= 0.
func (w *Worker) SetMaxBrainstormTurns(n int) {
	if n > 0 {
		w.maxBrainstormTurns.Store(int64(n))
	}
}
```
In `brainstorm()`, replace the cap check (was `if rec.BrainstormTurns >= w.maxBrainstormTurns {` and the `%d` Sprintf using `w.maxBrainstormTurns`):
```go
	maxTurns := int(w.maxBrainstormTurns.Load())
	if rec.BrainstormTurns >= maxTurns {
		msg := fmt.Sprintf("I've reached the question limit (%d rounds) on this card without a clear spec. It needs a human to decide the direction.", maxTurns)
		if err := w.board.PostComment(ctx, card.ID, msg); err != nil {
			return err
		}
		return w.board.MoveTo(ctx, card.ID, board.PhaseAwaitingAnswers)
	}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/orchestrator/ -v`
Expected: PASS (new test + existing, including `TestWorkerBrainstormCapSkipsClone` which uses `WithMaxBrainstormTurns`).

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/worker.go internal/orchestrator/worker_test.go
git commit -m "feat(orchestrator): hot-reloadable maxBrainstormTurns (atomic) + SetMaxBrainstormTurns"
```

---

## Task 5: `config.ResolvePath` + `config.Watch`

**Files:**
- Create: `internal/config/watch.go`
- Test: `internal/config/watch_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/config/watch_test.go`:

```go
package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wazir.yaml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResolvePath(p); !ok || got != p {
		t.Errorf("explicit flag: got %q ok=%v", got, ok)
	}
	if _, ok := ResolvePath(filepath.Join(dir, "missing.yaml")); ok {
		t.Error("missing explicit file should be ok=false")
	}
	t.Chdir(t.TempDir()) // no ./wazir.yaml, no $HOME config in a temp HOME
	t.Setenv("HOME", t.TempDir())
	if _, ok := ResolvePath(""); ok {
		t.Error("env-only run (no file) should be ok=false")
	}
}

func TestWatchReloadsOnChangeAndRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wazir.yaml")
	const valid = "github:\n  auth: pat\n  pat: tok\nproject:\n  owner: octocat\n  number: 1\n"
	if err := os.WriteFile(path, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloaded := make(chan Config, 1)
	errs := make(chan error, 1)
	go Watch(ctx, path, func(c Config) { reloaded <- c }, func(e error) { errs <- e })

	// Valid edit → onReload with the new value.
	if err := os.WriteFile(path, []byte(valid+"repos:\n  - octocat/added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-reloaded:
		if len(c.Repos) != 1 || c.Repos[0] != "octocat/added" {
			t.Errorf("reloaded repos = %v", c.Repos)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload")
	}

	// Invalid edit (number 0) → onError, not onReload.
	if err := os.WriteFile(path, []byte("github:\n  auth: pat\n  pat: tok\nproject:\n  owner: octocat\n  number: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errs:
	case c := <-reloaded:
		t.Fatalf("invalid config should not reload; got %+v", c)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for onError")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run 'TestResolvePath|TestWatch' -v`
Expected: compile error (`ResolvePath`, `Watch` undefined).

- [ ] **Step 3: Implement the watcher**

Create `internal/config/watch.go`:

```go
package config

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ResolvePath returns the config file Load(flagConfig) would read, and ok=false
// for an env-only run (no file to watch). When flagConfig is set it must exist.
func ResolvePath(flagConfig string) (path string, ok bool) {
	if flagConfig != "" {
		return flagConfig, fileExists(flagConfig)
	}
	if dir, name, found := defaultConfigFile(); found {
		return filepath.Join(dir, name), true
	}
	return "", false
}

// Watch reloads `path` whenever it changes, calling onReload(cfg) on a successful
// load+validate or onError(err) when the reloaded file is invalid. It watches the
// parent directory (so an editor's write-and-rename keeps the watch), debounces
// bursts, and returns when ctx is done. Logger-free by design.
func Watch(ctx context.Context, path string, onReload func(Config), onError func(error)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(filepath.Dir(path)); err != nil {
		return err
	}
	base := filepath.Base(path)
	const debounce = 200 * time.Millisecond
	var timer *time.Timer
	fire := make(chan struct{}, 1)
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if filepath.Base(ev.Name) != base ||
				ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(debounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		case <-fire:
			cfg, err := Load(path)
			if err != nil {
				onError(err)
				continue
			}
			onReload(cfg)
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			onError(err)
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/config/ -run 'TestResolvePath|TestWatch' -v`
Expected: PASS. (The Watch test uses real file writes + a 5s eventual timeout; it is not `-short`-gated.)

- [ ] **Step 5: Commit**

```bash
git add internal/config/watch.go internal/config/watch_test.go
git commit -m "feat(config): ResolvePath + fsnotify Watch (load+validate, debounced)"
```

---

## Task 6: `serve` — start the watcher + reload callback

**Files:**
- Modify: `cmd/wazir/serve.go`

(No unit test — daemon wiring, like the existing serve code; covered by `go build` + the Task 5 watcher test + manual.)

- [ ] **Step 1: Add the restart-only diff helper**

In `cmd/wazir/serve.go`, add `"strings"` to the imports and add this package-level helper. The `github`/`project`/`store`/`forge` sub-structs are all string/int fields, so `==` compares them (no `reflect`). `webhook_secret` lives under `github` but **is** hot-reloaded, so it's zeroed out before the github compare to avoid a false "restart needed" warning on a secret-only edit:

```go
// restartOnlyChanged reports which restart-only config groups differ between two
// loads (auth/board/store/forge/claude.bin), so a reload can warn that they were
// ignored. Returns "" when nothing restart-only changed.
func restartOnlyChanged(oldCfg, newCfg config.Config) string {
	var changed []string
	og, ng := oldCfg.GitHub, newCfg.GitHub
	og.WebhookSecret, ng.WebhookSecret = "", "" // hot-reloaded; exclude from the compare
	if og != ng {
		changed = append(changed, "github")
	}
	if oldCfg.Project != newCfg.Project {
		changed = append(changed, "project")
	}
	if oldCfg.Store != newCfg.Store {
		changed = append(changed, "store")
	}
	if oldCfg.Forge != newCfg.Forge {
		changed = append(changed, "forge")
	}
	if oldCfg.Claude.Bin != newCfg.Claude.Bin {
		changed = append(changed, "claude.bin")
	}
	return strings.Join(changed, ", ")
}
```

- [ ] **Step 2: Start the watcher after the worker is built**

In `runServe`, immediately after the `worker := orchestrator.NewWorker(...)...WithBase(...)` block (≈ line 91) and before the queue setup, insert:

```go
	// Live config reload: re-read wazir.yaml on change and hot-swap the safe
	// subset (claude.*, repos, bot_login, webhook_secret). Restart-only fields are
	// ignored with a warning. Disabled for an env-only run (no file to watch).
	if path, ok := config.ResolvePath(flagConfig); ok {
		startCfg := cfg
		go func() {
			err := config.Watch(ctx, path,
				func(newCfg config.Config) {
					if d := restartOnlyChanged(startCfg, newCfg); d != "" {
						logger.Warn("config change requires a restart; ignored", zap.String("fields", d))
					}
					brain.Reload(newCfg.Claude)
					b.Reload(newCfg.Repos, newCfg.BotLogin, newCfg.GitHub.WebhookSecret)
					worker.SetMaxBrainstormTurns(newCfg.Claude.MaxBrainstormTurns)
					logger.Info("config reloaded")
				},
				func(err error) { logger.Warn("config reload failed; keeping current config", zap.Error(err)) },
			)
			if err != nil {
				logger.Warn("live config reload disabled (watcher error)", zap.Error(err))
			}
		}()
	} else {
		logger.Info("live config reload disabled (no config file; env-only)")
	}
```

- [ ] **Step 3: Verify the build + vet**

Run: `go build ./... && go vet ./...`
Expected: clean. (`b`, `brain`, `worker` are the concrete `*GitHubBoard`/`*ClaudeBrain`/`*Worker`, so `.Reload`/`.SetMaxBrainstormTurns` resolve — same as the existing `b.Hydrate` call.)

- [ ] **Step 4: Commit**

```bash
git add cmd/wazir/serve.go
git commit -m "feat(serve): live config reload via fsnotify watcher (claude/repos/bot_login/webhook_secret)"
```

---

## Task 7: Docs

**Files:** `wazir.example.yaml`, `CLAUDE.md`

- [ ] **Step 1: Document in `wazir.example.yaml`**

Append a comment block near the `claude:` section:
```yaml
# Live reload: `wazir serve` watches this file and hot-applies changes to claude.*
# (except bin), repos, bot_login, and webhook_secret — no restart needed. Changes to
# github auth, project, store, forge, claude.bin, or --addr require a restart (logged).
```

- [ ] **Step 2: Note it in `CLAUDE.md`**

In the "Configuration (fig)" section, add one sentence: `wazir serve` live-reloads `wazir.yaml` (fsnotify) for the safe subset (`claude.*` minus `bin`, `repos`, `bot_login`, `webhook_secret`); other fields are restart-only.

- [ ] **Step 3: Verify + commit**

Run: `go build ./...`
```bash
git add wazir.example.yaml CLAUDE.md
git commit -m "docs: document live config reload + restart-only fields"
```

---

## Task 8: Full verification

**Files:** none.

- [ ] **Step 1: Build, test, vet, race**

Run:
```bash
go build ./...
go test ./...
go vet ./...
go test -race ./...
```
Expected: all green, no network/credentials.

- [ ] **Step 2: Dependency rule**

Run: `go test ./internal/orchestrator/ -run TestNoProviderImportsInOrchestrator -v`
Expected: PASS (config gained fsnotify, not a provider; the core is untouched).

- [ ] **Step 3: Integration compiles**

Run: `go vet -tags integration ./...`
Expected: clean.

- [ ] **Step 4: No stale field reads**

Run: `grep -rn "c\.model\b\|c\.planTimeout\|b\.repos\b\|b\.botLogin\b\|b\.webhookSecret\b\|w\.maxBrainstormTurns[^.]" internal/`
Expected: no matches outside the new snapshot/atomic accessors.

---

## Self-review notes (author)

- **Spec coverage:** §4.1 claude atomic → Task 2; §4.2 board atomic → Task 3; §4.3 worker atomic → Task 4; §5 ResolvePath+Watch → Task 5; §6 serve wiring + restart-only warning → Task 6; §7 error handling (invalid reload → onError → keep + warn; watcher setup failure → warn + continue; env-only → disabled) → Tasks 5–6; §8 testing → woven through + Task 8; new dep → Task 1; docs → Task 7.
- **Type consistency:** `claudeSettings`/`settingsFrom`/`ClaudeBrain.settings`/`Reload(config.ClaudeConfig)`, `boardReloadable`/`snap()`/`Reload(repos, botLogin, webhookSecret)`, `Worker.maxBrainstormTurns atomic.Int64`/`SetMaxBrainstormTurns(int)`, `config.ResolvePath(string)(string,bool)`/`config.Watch(ctx,string,func(Config),func(error))error` used identically across tasks. RunSpec fields (`Model`/`Timeout`/`AllowedTools`/`SettingSources`/`PluginsDir`/`EnabledPlugin`) match the existing runner.
- **Out of scope (spec §9):** auth/project/store/addr/claude.bin/forge.* reload; env reload; SIGHUP.
```
