# Live Config Reload for `wazir serve` (Design Spec)

**Date:** 2026-06-16
**Status:** Approved for planning
**Scope:** Let a running `wazir serve` pick up changes to the **safe subset** of `wazir.yaml`
without a restart — primarily the `claude.*` brain tunables (model, timeouts) plus the board's
`repos` / `bot_login` / `webhook_secret`. A `fsnotify` watcher re-loads the file on change and pushes
the new values into three components, each holding its reloadable state in an `atomic.Pointer`
(Approach A). Auth, project identity, the store, the listen address, and `claude.bin` stay
**restart-only**. **Source of truth:** the shipped code on `main`.

> **Motivation:** planning routinely exceeds the 10m `plan_timeout` default and the operator wants to
> switch model (e.g. `claude-sonnet-4-6`) and lengthen timeouts without bouncing the daemon and losing
> in-flight state. The immediate need (model + timeouts) is the smallest slice of this; the same
> mechanism covers the rest of the safe subset.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| Reload scope | **Safe hot-swap subset** | `claude.*` (minus `bin`), `repos`, `bot_login`, `webhook_secret`. These are read fresh per use and need no client rebuild. Auth/`project`/`store`/`addr`/`claude.bin`/`forge.*` stay restart-only (changing them needs rebuilding clients / the listener / the store, which is risky mid-flight). |
| Mechanism | **Approach A — per-component `atomic.Pointer[settings]`** | Each reloadable component owns an immutable settings snapshot behind an atomic pointer; readers `Load()`, reload `Store()`s. Lock-free, no torn reads, self-contained, unit-testable. (Rejected: a central RWMutex-guarded `*Config` — more coupling + read-path contention; and rebuild-and-swap components — overkill for swapping a handful of fields.) |
| Trigger | **`fsnotify` file watcher** | Auto-reload on `wazir.yaml` change. (Rejected: SIGHUP — chosen against in favor of zero-touch; polling — laggy.) Reload re-reads the **file**; env overrides are fixed for a running process. |
| Invalid reload | **Keep running settings, log a warning** | A bad edit never crashes the daemon and never partially applies. |
| In-flight turns | **Finish on the settings they started with** | A running `claude` already holds its `context.WithTimeout`; the *next* turn picks up new values. Not configurable. |
| Restart-only change | **Ignored on reload + warned** | If a restart-only field changed, log `"<field> change requires a restart; ignored"` so the operator isn't confused. |

---

## 2. Deliverable & demo

Edit `wazir.yaml` while `wazir serve` is running; within ~1s the new `claude.model` / `plan_timeout`
(and `repos` / `bot_login` / `webhook_secret`) take effect on the **next** turn/event — no restart, no
dropped in-flight work. Changes to restart-only fields are logged as ignored.

**Demo (acceptance):**
1. Start `serve`; trigger a plan turn → it uses the old model/timeout.
2. Edit `wazir.yaml`: `claude.model: claude-sonnet-4-6`, `claude.plan_timeout: 30m`. Save.
3. Logs show `config reloaded`. The next plan turn runs with `--model claude-sonnet-4-6` and the 30m
   deadline. No restart; the queue/listener/store are untouched.
4. Add a repo to `repos` and save → a card in that repo (previously `repo not in allow-list`) is now
   accepted on its next event.
5. Edit a restart-only field (e.g. `store.db_path`) → log warns it's ignored pending a restart; the
   daemon keeps running on the old value.
6. Introduce a config error (e.g. delete `project.owner`) → log warns the reload was rejected; the
   running settings are unchanged.

---

## 3. Architecture

```
wazir.yaml ──fsnotify──▶ config.Watch ──onReload(cfg)──▶ serve callback
                                                            ├─ brain.Reload(cfg.Claude)            (atomic.Pointer[claudeSettings])
                                                            ├─ board.Reload(repos, botLogin, secret) (atomic.Pointer[boardReloadable])
                                                            └─ worker.SetMaxBrainstormTurns(n)       (atomic.Int64)
```

`config.Watch` owns all `fsnotify` realities and stays logger-free; `serve` owns the callback that
applies the reload and logs. Each component reads its reloadable fields from an atomic snapshot on
every use, so a `Store` from the watcher goroutine is observed by the worker/webhook goroutines without
a lock. The dependency rule is unchanged: `config` imports no provider; `serve` (cmd/wazir) is the only
place that knows all three components.

---

## 4. The three reloadable components

### 4.1 `internal/claude.ClaudeBrain`
Collapse the eight captured fields into an immutable snapshot:

```go
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
type ClaudeBrain struct {
	runner   *Runner                       // bin is fixed (restart-only)
	settings atomic.Pointer[claudeSettings]
	log      *zap.Logger
}
func settingsFrom(cfg config.ClaudeConfig) *claudeSettings { /* maps cfg → snapshot, splitTools */ }
func New(cfg config.ClaudeConfig, log *zap.Logger) *ClaudeBrain { b := &ClaudeBrain{...}; b.settings.Store(settingsFrom(cfg)); return b }
func (c *ClaudeBrain) Reload(cfg config.ClaudeConfig) { c.settings.Store(settingsFrom(cfg)) }
```
`Brainstorm`/`Plan`/`Execute` each begin with `s := c.settings.Load()` and read `s.model`, `s.planTimeout`,
etc. when building the `RunSpec`. No behavior change beyond the indirection.

### 4.2 `internal/board/github.GitHubBoard`
Move the three reloadable fields off the struct into a snapshot:

```go
type boardReloadable struct {
	repos         []string
	botLogin      string
	webhookSecret string
}
// struct: reloadable atomic.Pointer[boardReloadable]   (replaces the repos/botLogin/webhookSecret fields)
func (b *GitHubBoard) Reload(repos []string, botLogin, webhookSecret string) {
	b.reloadable.Store(&boardReloadable{repos: slices.Clone(repos), botLogin: botLogin, webhookSecret: webhookSecret})
}
```
`repoAllowed`, `ParseEvent` (signature validation + bot filter + repo filter) read `b.reloadable.Load()`.
`New` stores the initial snapshot from cfg. This is the component read **concurrently** by the webhook
HTTP goroutine and the worker goroutines, so the atomic is load-bearing here.

### 4.3 `internal/orchestrator.Worker`
`maxBrainstormTurns int` → `maxBrainstormTurns atomic.Int64`. `brainstorm()` reads `int(w.maxBrainstormTurns.Load())`.
`WithMaxBrainstormTurns(n)` and a new `SetMaxBrainstormTurns(n int)` both `Store` (ignoring `n <= 0`).

---

## 5. The watcher (`internal/config`)

```go
// ResolvePath returns the file Load would read for the given --config flag: the flag
// when set, else the discovered default (./wazir.yaml then ~/.config/wazir/wazir.yaml).
// ok=false for an env-only run (no file) — the caller skips watching.
func ResolvePath(flagConfig string) (path string, ok bool)

// Watch re-loads `path` on change and calls onReload(cfg) on a successful load+validate,
// or onError(err) when the reloaded file is invalid (the caller keeps the running config).
// It returns when ctx is done. Logger-free by design.
func Watch(ctx context.Context, path string, onReload func(Config), onError func(error)) error
```

`Watch` behavior:
- Watches **`filepath.Dir(path)`** (not the file) so an editor's write-temp-then-rename doesn't drop
  the watch; filters events whose basename ≠ the config file's.
- **Debounces:** a 200ms timer, reset per event, coalesces the burst a single save emits.
- On the settled change: `cfg, err := Load(path)`; `err != nil` → `onError(err)`; else `onReload(cfg)`.
- Stops and closes the `fsnotify.Watcher` on `ctx.Done()`.

---

## 6. Wiring (`cmd/wazir/serve.go`)

After building board/brain/worker and before/alongside the queue+server run loop:

```go
if path, ok := config.ResolvePath(flagConfig); ok {
	startCfg := cfg // captured for restart-only change detection
	go config.Watch(ctx, path,
		func(newCfg config.Config) {
			if diff := restartOnlyChanged(startCfg, newCfg); diff != "" {
				logger.Warn("config change requires a restart; ignored", zap.String("fields", diff))
			}
			brain.Reload(newCfg.Claude)
			b.Reload(newCfg.Repos, newCfg.BotLogin, newCfg.GitHub.WebhookSecret)
			worker.SetMaxBrainstormTurns(newCfg.Claude.MaxBrainstormTurns)
			logger.Info("config reloaded")
		},
		func(err error) { logger.Warn("config reload failed; keeping current config", zap.Error(err)) },
	)
} else {
	logger.Info("live config reload disabled (no config file; env-only)")
}
```
`restartOnlyChanged(old, new)` compares `github` (auth/app_id/installation_id/private_key/owner_type),
`project`, `store`, `forge`, and `claude.bin`, returning a comma-joined list of changed groups (or "").
The HTTP listen addr is a flag, not in the file, so it can't change via reload.

---

## 7. Error handling

| Situation | Handling |
|---|---|
| Reloaded config fails `Load`/`validate` | `onError` → log warning; running settings unchanged; daemon continues. |
| Restart-only field changed | Applied fields still reload; a warning names the ignored groups. |
| `fsnotify` setup fails (can't watch) | `Watch` returns an error; `serve` logs a warning and continues **without** live reload (still serves). |
| No config file (env-only) | Watcher not started; logged once. |
| In-flight turn during reload | Unaffected (keeps its started settings); next turn uses new ones. |
| Reloaded `claude.pluginsDir` | Not re-checked for existence at reload (only at `serve` startup); an invalid value fails the next plan/execute turn loudly (the per-run seeding step), not the reload itself. |

---

## 8. Testing strategy

- **claude (`internal/claude`):** after `Reload`, a turn driven against the fake `CLAUDE_BIN` emits the
  new `--model` and uses the new timeout; a `-race` test calls `settings.Load` in a tight loop while
  `Reload` stores, asserting no race and a coherent snapshot.
- **board (`internal/board/github`):** `repoAllowed` and `ParseEvent` reflect a reloaded `repos` /
  `bot_login` / `webhook_secret` (e.g. a signature that validates only under the new secret); `-race`
  concurrent `ParseEvent` + `Reload`.
- **worker (`internal/orchestrator`):** `SetMaxBrainstormTurns` lowering the cap mid-run escalates at the
  new value; `WithMaxBrainstormTurns` still works.
- **config (`internal/config`):** `ResolvePath` for {`--config` set, default discovered, env-only}; and
  `Watch` — a temp file edited fires `onReload` with the new values (eventual-poll assertion, generous
  timeout); an edit that fails validation fires `onError`, not `onReload`. (The load+validate decision is
  deterministic; only the fsnotify event delivery is timing-sensitive.)
- `go test ./...` stays network-/credential-free; `go vet ./...` clean; `go test -race ./...` clean (the
  race detector guards the concurrent Load/Store paths).

---

## 9. Out of scope

- **Restart-only fields:** `github` auth, `project`, `store`, `serve --addr`, `claude.bin`, `forge.*` —
  changing these needs a process restart (warned on reload). Hot-reloading auth/board would require
  rebuilding the `ghinstallation` transport + board/forge clients and re-hydrating mid-flight — a much
  larger, riskier change, deliberately deferred.
- **Reload of env vars:** a process's environment is fixed; reload re-reads the file only.
- **SIGHUP / manual reload command:** not built; the watcher is automatic.

---

## 10. Package layout (delta)

```
✚ internal/config/watch.go            # ResolvePath + Watch (fsnotify); logger-free
✚ internal/config/watch_test.go       # ResolvePath cases + Watch onReload/onError
✎ internal/claude/brain.go            # claudeSettings + atomic.Pointer; New stores; Reload; turns Load
✎ internal/claude/brain_test.go       # Reload swaps model/timeout; -race Load/Store
✎ internal/board/github/board.go      # boardReloadable + atomic.Pointer; New stores; Reload; readers Load
✎ internal/board/github/board_test.go (+ parse_event_test.go) # reloaded repos/botLogin/secret; -race
✎ internal/orchestrator/worker.go     # maxBrainstormTurns -> atomic.Int64; SetMaxBrainstormTurns
✎ internal/orchestrator/worker_test.go# cap change mid-run
✎ cmd/wazir/serve.go                  # ResolvePath + go config.Watch(...) with the reload callback
✎ wazir.example.yaml, CLAUDE.md       # document live reload + the restart-only list
+  go.mod / go.sum                     # add github.com/fsnotify/fsnotify
```

---

## 11. Acceptance checklist

- [ ] `ClaudeBrain` holds an `atomic.Pointer[claudeSettings]`; turns read a `Load()` snapshot; `Reload`
      swaps it; `bin` stays fixed.
- [ ] `GitHubBoard` reads `repos`/`botLogin`/`webhookSecret` from `atomic.Pointer[boardReloadable]`;
      `Reload` swaps it (cloning the slice).
- [ ] `Worker.maxBrainstormTurns` is an `atomic.Int64`; `SetMaxBrainstormTurns` + `WithMaxBrainstormTurns` store it.
- [ ] `config.ResolvePath` + `config.Watch` (fsnotify, parent-dir watch, debounce, load+validate,
      onReload/onError) — no provider import, no logger.
- [ ] `serve` starts the watcher when a config file exists, applies reloads via the three `Reload`s,
      warns on restart-only changes, and logs `config reloaded`; disabled (logged) for env-only runs.
- [ ] Invalid reload keeps the running config and logs a warning; daemon never crashes.
- [ ] `go test ./...` green; `go vet ./...` clean; `go test -race ./...` clean; `imports_test.go` green.
- [ ] `go.mod` adds `github.com/fsnotify/fsnotify`.
- [ ] Docs updated (live reload + restart-only list).
