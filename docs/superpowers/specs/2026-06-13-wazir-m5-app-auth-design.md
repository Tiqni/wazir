# Wazir M5 (slice 2) — GitHub App Token Auth (Design Spec)

**Date:** 2026-06-13
**Status:** Approved for planning
**Scope:** The second slice of milestone **M5** (`docs/wazir-init-plan.md` §10 "Hardening", §13 the
`auth: app` scaffold). Replace the long-lived **PAT** with a GitHub **App installation token** as the
single auth for *every* GitHub-side operation: the Projects v2 board (GraphQL), issues/PRs (REST), and
**git** (clone / fetch / push). One `ghinstallation.Transport` mints and auto-refreshes the hourly
installation token and feeds all three consumers. **PAT support is removed entirely** — App is the only
auth mode. Pure Go; the one new dependency is `github.com/bradleyfalzon/ghinstallation/v2`.
**Source of truth:** `docs/wazir-init-plan.md` and the shipped M0–M5(slice 1) specs + code. Where the
init plan and the shipped code disagree, the code wins.

> **M5 is independent slices, not one project.** Slice 1 (execution isolation) is merged. This spec covers
> **App-token auth** only. The remaining items — *observability & cost*, *resilience*, and *label-based
> approval* (the other half of the original "approval & auth ergonomics" grouping) — get their own
> spec → plan → implementation cycles.

This spec turns the slice into a buildable, testable unit. It records the brainstorming decisions and the
design that follows. It does not restate the architecture — read the init plan and the M0–M5 specs.

---

## 1. Decisions (settled in brainstorming)

| Decision | Choice | Notes |
|---|---|---|
| Auth mechanism | **One shared `ghinstallation.Transport`** | The transport mints + caches + auto-refreshes the ~1h installation token. It serves the board/forge `*http.Client` (sets `Authorization: token <inst>`) **and** exposes `Token(ctx)` for the git side. One transport, one cache, three consumers — no double-minting. |
| Git auth under hourly expiry | **A token *source*, not a static string** | The forge's `gitRunner` holds a `func(ctx) (string, error)` and resolves a fresh token at the moment of each network op, then injects it as the existing `http.extraHeader` `x-access-token:<token>`. Captures the expiry problem at the only place it matters. |
| PAT support | **Removed entirely** | App is the sole auth mode; the `Auth`/`PAT` config fields, the `bearerTransport`, `ErrAppAuthNotWired`, and the PAT unit/integration test paths are deleted. YAGNI — the deployment uses App auth exclusively. |
| Private key delivery | **Auto-detect (path or PEM)** | If `private_key` resolves to an existing file → `ghinstallation.NewKeyFromFile`; else treat the value as PEM bytes (base64-decode first if it decodes cleanly) → `ghinstallation.New`. Works for a VPS `.pem` file and a containerized env var alike. |
| Installation id | **Required in config** | `app_id` + `installation_id` + `private_key` are all required (no auto-discovery). Deterministic, no startup discovery call. |
| Startup behaviour | **Eager mint, fail loud** | `serve` mints one installation token before listening; a bad key / wrong id dies at startup, not mid-turn (mirrors slice 1's plugin-dir resolution). |
| Account topology | **Board + repos under one org; single installation** | A GitHub App installation is *account-scoped* (only that account's repos + org projects), unlike a user PAT which is user-scoped. Consolidating the board and all operated repos under one org makes a **single** installation cover everything. Multi-account / multi-installation routing is out of scope (a future seam, like multi-board §4.1). |

### 1.1 Why an org, and why a single installation (spike-driven)

A **user-owned** Projects v2 board is **invisible to a GitHub App** installation token: a `projectV2`
GraphQL read returns `Could not resolve to a ProjectV2 with the number N` (GitHub hides existence rather
than returning a permission error). This was confirmed live on 2026-06-13 against board #5 — a PAT read it
fine; the App could not (see §9, and the spike runbook
`docs/superpowers/spikes/2026-06-13-m5-slice2-app-auth-spike.md`). The board must therefore move to an
**org** for App auth to drive it at all. Because installation tokens are account-scoped, the **repos** the
App operates on must live under that **same** org for one installation to cover both the board and git/
issue/PR access. Hence the decision: consolidate board + repos under one org; configure one installation.

---

## 2. Deliverable & demo

This slice makes every GitHub-side operation authenticate with an App installation token from a single
shared transport, and removes PAT. Changes live in `internal/githubauth`, `internal/config`,
`internal/forge`, `internal/forge/github`, and `cmd/wazir` — no store/board state changes, no migration of
on-disk data.

**Demo (acceptance):**

1. **One transport, three consumers.** With `auth`-by-App configured, `wazir provision`/`bootstrap`/`serve`
   reach the org board (GraphQL), issues/PRs (REST), and git (clone/fetch/push) using installation tokens
   minted by a single `ghinstallation.Transport`.
2. **Hourly refresh is transparent.** A clone/push that happens >1h after the previous one fetches a fresh
   token via the git token source; nothing is cached stale in the forge.
3. **No secret leakage (unchanged).** Git still injects the token via `http.extraHeader` (not argv, not
   `.git/config`); the curated git env still drops `WAZIR_*`. The token is short-lived and account-scoped.
4. **Fails loud at startup.** A wrong `installation_id` or unparseable `private_key` fails `wazir serve`
   before it listens, with a fix-it message — never mid-turn.
5. **PAT is gone.** No `auth`/`pat` config, no `WAZIR_GITHUB_PAT`; the build has no PAT code path.
6. **Offline tests.** `go test ./...` drives the App path against an in-test generated RSA key and a
   `GitToken` stub; no network or real credentials.

---

## 3. Architecture — the auth seam

### 3.1 The `Auth` bundle

`internal/githubauth` returns a small bundle built from one transport:

```go
// Auth carries the two auth surfaces the daemon needs, both backed by one
// ghinstallation.Transport so the installation token is minted/refreshed once.
type Auth struct {
    HTTPClient *http.Client                              // board REST+GraphQL AND forge REST (PRs)
    GitToken   func(ctx context.Context) (string, error) // a fresh installation token per git network op
}

// New builds the shared transport from the App config and returns the bundle.
func New(ctx context.Context, cfg config.Config) (Auth, error)

// HTTPClient is a convenience for API-only callers (provision, card) that need
// just the client. It returns New(ctx, cfg).HTTPClient.
func HTTPClient(ctx context.Context, cfg config.Config) (*http.Client, error)
```

`New` builds `tr, err := <ghinstallation transport>` once, then:
- `HTTPClient = &http.Client{Transport: tr}` — `ghinstallation` sets `Authorization: token <inst>` on every
  request and refreshes the token under the hood.
- `GitToken = tr.Token` — `(*ghinstallation.Transport).Token(ctx) (string, error)` returns the current
  cached/refreshed installation token, i.e. the *same* token the HTTP client uses.

### 3.2 Per-consumer token flow

| Consumer | How it authenticates |
|---|---|
| Board — Projects v2 GraphQL (`shurcooL/githubv4`) | `Auth.HTTPClient` |
| Board — issues/comments REST (`go-github`) | `Auth.HTTPClient` |
| Forge — open PR REST (`go-github`) | `github.NewClient(Auth.HTTPClient)` |
| Forge — git clone/fetch/push | `Auth.GitToken(ctx)` → `http.extraHeader` `AUTHORIZATION: basic base64("x-access-token:" + token)` |

**Dependency rule (init-plan §4.2/§5.1):** unchanged. `internal/forge` receives a plain
`func(ctx) (string, error)` — no provider concept crosses the port. `cmd/wazir` remains the only package
that imports `internal/githubauth` + `ghinstallation`. The orchestrator core is untouched;
`imports_test.go` stays green.

---

## 4. Package layout (delta on M5 slice 1)

```
✎ internal/githubauth/auth.go         # App-only: build ghinstallation.Transport; Auth bundle; key auto-detect; drop PAT/bearerTransport/ErrAppAuthNotWired
✎ internal/githubauth/auth_test.go    # drop PAT + AppNotWired tests; add App-path + key auto-detect tests (in-test RSA key)
✎ internal/config/config.go           # GitHubConfig: drop Auth + PAT; require app_id/installation_id/private_key; owner_type=org expected
✎ internal/config/config_test.go      # validation: App fields required; drop PAT cases
✎ internal/forge/github/forge.go      # Options.Token string -> Options.GitToken func(ctx)(string,error)
✎ internal/forge/github/git.go        # gitRunner.token string -> func(ctx)(string,error); resolve token in run() before building authConfigEnv
✎ internal/forge/github/git_test.go   # auth-header test: pass a GitToken stub; assert error path + non-network ops never call it
✎ internal/forge/github/forge_git_test.go, integration_test.go # construct with GitToken; integration uses App env
✎ internal/board/github/integration_test.go # switch from WAZIR_GITHUB_PAT to App env
✎ cmd/wazir/serve.go                  # githubauth.New once; pass HTTPClient to board + HTTPClient+GitToken to forge; eager-mint at startup
✎ cmd/wazir/provision.go, card.go     # unchanged call shape (githubauth.HTTPClient wrapper)
✎ wazir.example.yaml, CLAUDE.md       # document App config; remove PAT; note org topology + M5 slice-2 status
+  go.mod / go.sum                     # add github.com/bradleyfalzon/ghinstallation/v2
```

---

## 5. `githubauth` — App transport + private-key auto-detect (`internal/githubauth/auth.go`)

- **Remove** `bearerTransport`, `ErrAppAuthNotWired`, and the `cfg.GitHub.Auth` `switch`.
- **`New(ctx, cfg)`**:
  1. `key, err := loadPrivateKey(cfg.GitHub.PrivateKey)` (auto-detect, below).
  2. `tr, err := ghinstallation.New(http.DefaultTransport, cfg.GitHub.AppID, cfg.GitHub.InstallationID, key)`.
     (`NewKeyFromFile` is the file-path convenience; auto-detect lets us read the file ourselves and always
     call `New(..., keyBytes)`, keeping one code path.)
  3. Return `Auth{HTTPClient: &http.Client{Transport: tr}, GitToken: tr.Token}`.
- **`loadPrivateKey(v string) ([]byte, error)`** — auto-detect:
  - If `v` names an existing file (`os.Stat`) → `os.ReadFile(v)`.
  - Else if `base64.StdEncoding.DecodeString(v)` succeeds and yields a PEM block → use the decoded bytes.
  - Else → treat `v` itself as PEM bytes (`[]byte(v)`).
  - `loadPrivateKey` returns only the bytes; `ghinstallation.New` parses them and returns an error for an
    invalid PEM, which `New` wraps with context (`"parse app private key: %w"`). No separate jwt import.
- **`HTTPClient(ctx, cfg)`** stays as a thin wrapper returning `New(ctx, cfg).HTTPClient`, so `provision`
  and `card` (API-only) don't change.

---

## 6. Forge — `GitToken` token source (`internal/forge/github`)

- **`Options.Token string` → `Options.GitToken func(ctx) (string, error)`.**
- **`gitRunner.token string` → `gitRunner.token func(ctx) (string, error)`.**
- **`run(ctx, dir, auth, args...)`**: when `auth` is true and `g.token != nil`, resolve the token first:
  ```go
  var authEnv []string
  if auth && g.token != nil {
      tok, err := g.token(ctx)
      if err != nil {
          return "", fmt.Errorf("get git token: %w", err)
      }
      authEnv = authConfigEnv(tok)
  }
  ```
  then append `authEnv` to the curated git env. `authConfigEnv(token string)` keeps today's signature and
  body (`x-access-token:<token>` via `GIT_CONFIG_*`). Non-network ops (`auth=false`) never call the source.
- The token is fetched **at the moment of use**, so the ~1h installation-token expiry is a non-issue: a
  clone today and a push next hour each get a current token.

---

## 7. Config — App fields, PAT removed (`internal/config`)

`GitHubConfig` after this slice:

```go
type GitHubConfig struct {
    OwnerType      string `fig:"owner_type" default:"org"` // org (App installs are account-scoped; board lives under the org)
    WebhookSecret  string `fig:"webhook_secret"`           // WAZIR_GITHUB_WEBHOOK_SECRET
    AppID          int64  `fig:"app_id"`                   // WAZIR_GITHUB_APP_ID
    InstallationID int64  `fig:"installation_id"`          // WAZIR_GITHUB_INSTALLATION_ID
    PrivateKey     string `fig:"private_key"`              // WAZIR_GITHUB_PRIVATE_KEY — .pem path or PEM bytes (base64-aware)
}
```

- **Removed:** `Auth` and `PAT`.
- **`validate()`**: require `AppID != 0 && InstallationID != 0 && PrivateKey != ""` (one combined error
  listing what's missing). Drop the `pat`/`app` `switch` and the PAT branch. `owner_type` must be `org`
  (a `user` board can't be driven by an App — §9); `user` becomes a validation error with a pointer to the
  org-topology prerequisite.
- No new validation network calls — key parseability is surfaced by `githubauth.New` at `serve` startup, not
  at `config.Load` (keeps `config` tests offline).

---

## 8. `serve`/`provision`/`card` wiring (`cmd/wazir`)

- **`serve`**: one `auth, err := githubauth.New(ctx, cfg)`. Board ← `auth.HTTPClient`; forge ←
  `github.NewClient(auth.HTTPClient)` + `GitToken: auth.GitToken`. **Eager mint**: call
  `auth.GitToken(ctx)` once before listening and fail loud on error (`fmt.Errorf("mint installation token
  (check app_id/installation_id/private_key): %w", err)`).
- **`provision`/`card`**: keep calling `githubauth.HTTPClient(ctx, cfg)` (the wrapper) — they only need the
  API client, not the git token. No shape change.

---

## 9. Spike findings (reference) — GitHub Apps can't read user-owned Projects v2

Confirmed live 2026-06-13 (runbook + output:
`docs/superpowers/spikes/2026-06-13-m5-slice2-app-auth-spike.md`):

- Minting an installation token via `ghinstallation` **works** (the git-side path).
- An App installation token reading **user-owned** board #5 via GraphQL **fails** with `Could not resolve
  to a ProjectV2 with the number 5` — while a PAT reads it fine. GitHub hides user-owned project existence
  from an App rather than returning a permission error. App installation tokens can drive **org**-owned
  Projects v2 only.

This is *why* the topology decision (board + repos under one org) is load-bearing, not cosmetic. See the
`github-app-user-projects-v2-limit` note; recheck if GitHub lifts the limitation.

---

## 10. Error handling summary

| Failure | Handling |
|---|---|
| `private_key` unparseable / file missing | `githubauth.New` error → `serve`/`provision` **startup** error with a fix-it message. |
| Wrong `app_id`/`installation_id` (mint rejected) | The eager mint in `serve` (or the first API call in `provision`) fails loudly at startup. |
| Token mint fails mid-run (transient/network) | Surfaces from `GitToken(ctx)` as the git op's error → phase fails → `Failed` (existing path). Retry/backoff is the separate *resilience* slice. |
| `app_id`/`installation_id`/`private_key` missing | `config.validate` error at `Load`. |
| `owner_type: user` configured | `config.validate` error pointing at the org-topology prerequisite (§13). |

---

## 11. Testing strategy

- **`githubauth` (offline):** generate an RSA key in-test (`rsa.GenerateKey` → PEM); assert `New` returns an
  `Auth` with a non-nil `GitToken` and an `HTTPClient` whose transport is the `ghinstallation.Transport`.
  `loadPrivateKey` table test over {existing file path, raw PEM bytes, base64-encoded PEM} → all yield the
  same key; a garbage value → error. (No token mint — that needs the network; covered by the integration
  test.) **Delete** `TestPATClientSetsBearerHeader` and `TestAppNotWiredYet`.
- **Forge (offline):** the existing auth-header test passes a `GitToken` stub returning `"tok"` and asserts
  the `x-access-token:tok` `http.extraHeader`; add a stub returning an error → the network op fails with it;
  assert non-network ops (`worktree`, `reset`) never invoke the stub (e.g. a stub that fails the test if
  called).
- **Config:** App fields required (missing any → error); `owner_type: user` → error; defaults + `WAZIR_*`
  overrides for the App fields. Drop PAT cases.
- **Build-tagged integration (`-tags integration`, not in CI):** `internal/githubauth` and the existing
  `board/github` + `forge/github` integration tests mint a **real** installation token from App env
  (`WAZIR_GITHUB_APP_ID` / `WAZIR_GITHUB_INSTALLATION_ID` / `WAZIR_GITHUB_PRIVATE_KEY`) against an org board
  and confirm a board read + a git op authenticate. Skip when unset.
- `go test ./...` stays network-/credential-free; `go vet ./...` clean; `go test -race ./...` clean;
  `go vet -tags integration ./...` compiles.

---

## 12. Out of this slice's scope

Deferred as deliberate seams, not gaps:

- **Multi-account / multiple installations** — one org installation only. Routing a token per repo-owner is
  a future seam (like multi-board routing, §4.1).
- **Label-based approval** — the other half of the original "approval & auth ergonomics" grouping; its own
  spec.
- **Retry/backoff on a transient token-mint failure** — the *resilience* M5 slice.
- **The org migration itself** — moving board #5 + repos under the org and installing the App is an
  **operational prerequisite** (§13), documented, not slice code.

---

## 13. Operational prerequisites (App auth live run)

1. **Create an org** (a free personal org is fine) and move/create the board there (`owner_type=org`,
   `project.owner=<org>`); move/create the operated repos under the same org.
2. **Register a GitHub App** (see the spike runbook for the field-by-field walkthrough) with repository
   permissions Contents RW, Issues RW, Pull requests RW, Metadata RO, and Organization → Projects RW;
   subscribe to Issues, Issue comment, Projects v2 events.
3. **Generate a private key** (`.pem`) and **install the App on the org**, granting the operated repos.
4. Configure `app_id`, `installation_id` (from the install URL), and `private_key` (path or PEM bytes);
   set the webhook URL + secret. Re-run `wazir provision`/`bootstrap`.
5. The human spec-approval gate remains the security boundary before any code runs (unchanged).

---

## 14. Acceptance checklist

- [ ] `githubauth.New(ctx, cfg)` builds one `ghinstallation.Transport` and returns `Auth{HTTPClient,
      GitToken}`; `HTTPClient(ctx, cfg)` wrapper preserved for API-only callers.
- [ ] Private-key auto-detect handles {file path, PEM bytes, base64 PEM}; garbage → error.
- [ ] `bearerTransport`, `ErrAppAuthNotWired`, the `Auth`/`PAT` config fields, and the PAT tests are removed.
- [ ] Forge `Options.GitToken` + `gitRunner.token` are `func(ctx) (string, error)`; `run` resolves a fresh
      token per network op and injects it via `http.extraHeader`; non-network ops don't.
- [ ] `config.validate` requires `app_id`/`installation_id`/`private_key` and rejects `owner_type: user`.
- [ ] `serve` wires one `githubauth.New`, passes the client to the board and client+token-source to the
      forge, and eager-mints at startup (fail loud).
- [ ] `go.mod` adds `github.com/bradleyfalzon/ghinstallation/v2`.
- [ ] `internal/orchestrator` still imports no provider package (`imports_test.go` green).
- [ ] `go test ./...` green (offline); `go vet ./...` clean; `go test -race ./...` clean;
      `go vet -tags integration ./...` compiles.
- [ ] `CLAUDE.md` + `wazir.example.yaml` document App config + org topology and no longer mention PAT.
- [ ] Manual: against an org board + App install, `provision` + a brainstorm→PR cycle authenticate end to
      end on installation tokens; a >1h-later push still authenticates.
