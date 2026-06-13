# Wazir M5 (slice 2) — GitHub App Token Auth Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the PAT with a GitHub **App installation token** as the single auth for the board (GraphQL), issues/PRs (REST), and git (clone/fetch/push); remove PAT entirely.

**Architecture:** One `ghinstallation.Transport` mints + auto-refreshes the ~1h installation token and feeds both the API `*http.Client` and a git token *source* `func(ctx) (string, error)`. The forge resolves a fresh token at each git network op (so hourly expiry is a non-issue). `serve` eager-mints once at startup to fail loud.

**Tech Stack:** Go 1.24, `github.com/bradleyfalzon/ghinstallation/v2` (new), `github.com/google/go-github/v66`, `github.com/shurcooL/githubv4`, `kkyr/fig`, `os/exec`, `go.uber.org/zap`.

**Spec:** `docs/superpowers/specs/2026-06-13-wazir-m5-app-auth-design.md`. **Branch:** `m5-app-auth` (already created).

---

## File structure (what each task touches)

| File | Responsibility | Task |
|---|---|---|
| `go.mod` / `go.sum` | add `ghinstallation/v2` | 1 |
| `internal/githubauth/auth.go` | App-only: build the transport; `Auth{HTTPClient, GitToken}`; private-key auto-detect | 2 |
| `internal/githubauth/auth_test.go` | `New` + `loadPrivateKey` tests (in-test RSA key); drop PAT/AppNotWired tests | 2 |
| `internal/forge/github/forge.go` | `Options.GitToken` replaces `Options.Token` | 3 |
| `internal/forge/github/git.go` | `gitRunner.token` is a func; `run` resolves a token per network op | 3 |
| `internal/forge/github/git_test.go` | runner resolves/skips/propagates token-source | 3 |
| `internal/forge/github/forge_git_test.go` | construct forge with `GitToken` | 3 |
| `internal/forge/github/integration_test.go` | mint a real installation token from App env | 3 |
| `cmd/wazir/serve.go` | one `githubauth.New`; wire client + token source; eager-mint | 3 |
| `internal/config/config.go` | drop `Auth`/`PAT`; require App fields; `owner_type=org` | 4 |
| `internal/config/config_test.go`, `config_number_test.go` | App-env validation; drop PAT cases | 4 |
| `internal/board/github/integration_test.go` | doc comment → App env | 4 |
| `wazir.example.yaml`, `CLAUDE.md` | document App config; remove PAT | 5 |

**Dependency rule:** `internal/forge` receives a plain `func(ctx) (string, error)` — no provider type crosses the port; `cmd/wazir` stays the only importer of `githubauth`/`ghinstallation`. `internal/orchestrator/imports_test.go` stays green.

**Task ordering is compile-stable:** after each task the tree builds. Task 2 leaves the old `Auth`/`PAT` config fields in place (now unused by `githubauth`); Task 3 stops `serve` referencing `cfg.GitHub.PAT`; Task 4 then removes the fields with no dangling references.

---

## Task 1: Add the ghinstallation dependency

**Files:** `go.mod`, `go.sum`

- [ ] **Step 1: Add the module**

Run:
```bash
go get github.com/bradleyfalzon/ghinstallation/v2@latest
go mod tidy
```
Expected: `go.mod` gains `github.com/bradleyfalzon/ghinstallation/v2` in the `require` block; `go.sum` gains it plus `github.com/golang-jwt/jwt/v4`.

- [ ] **Step 2: Verify the build still passes**

Run: `go build ./...`
Expected: clean (nothing imports it yet).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add bradleyfalzon/ghinstallation/v2 for GitHub App auth"
```

---

## Task 2: `githubauth` — App-only `Auth` bundle + private-key auto-detect

**Files:**
- Modify: `internal/githubauth/auth.go`
- Test: `internal/githubauth/auth_test.go`

- [ ] **Step 1: Replace the test file**

Overwrite `internal/githubauth/auth_test.go` with:

```go
package githubauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// testKeyPEM generates a throwaway RSA private key in PKCS#1 PEM form.
func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func appConfig(privateKey string) config.Config {
	return config.Config{GitHub: config.GitHubConfig{AppID: 1, InstallationID: 2, PrivateKey: privateKey}}
}

func TestNewBuildsAppAuth(t *testing.T) {
	a, err := New(context.Background(), appConfig(string(testKeyPEM(t))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.HTTPClient == nil {
		t.Error("HTTPClient must be non-nil")
	}
	if a.GitToken == nil {
		t.Error("GitToken must be non-nil")
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	if _, err := New(context.Background(), appConfig("not-a-pem-key")); err == nil {
		t.Fatal("expected an error for an unparseable private key")
	}
}

func TestLoadPrivateKeyAutoDetect(t *testing.T) {
	pemBytes := testKeyPEM(t)
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"file path":  path,
		"raw PEM":    string(pemBytes),
		"base64 PEM": base64.StdEncoding.EncodeToString(pemBytes),
	}
	for name, v := range cases {
		got, err := loadPrivateKey(v)
		if err != nil {
			t.Fatalf("%s: loadPrivateKey: %v", name, err)
		}
		if !bytes.Equal(got, pemBytes) {
			t.Errorf("%s: got %d bytes, want the original PEM", name, len(got))
		}
	}
}

func TestLoadPrivateKeyEmpty(t *testing.T) {
	if _, err := loadPrivateKey(""); err == nil {
		t.Fatal("expected an error for an empty private key")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/githubauth/ -v`
Expected: compile error (`undefined: New`, `undefined: loadPrivateKey`, `GitToken`).

- [ ] **Step 3: Rewrite `auth.go`**

Overwrite `internal/githubauth/auth.go` with:

```go
// Package githubauth produces GitHub App installation auth: an authenticated
// *http.Client for the REST + GraphQL clients and a token source for git.
// One ghinstallation.Transport mints and auto-refreshes the ~1h installation
// token and backs both.
package githubauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	"github.com/bradleyfalzon/ghinstallation/v2"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// Auth carries the two auth surfaces the daemon needs, both backed by one
// ghinstallation.Transport so the installation token is minted/refreshed once.
type Auth struct {
	HTTPClient *http.Client                              // board REST+GraphQL AND forge REST (PRs)
	GitToken   func(ctx context.Context) (string, error) // a fresh installation token per git network op
}

// New builds the shared installation transport from the App config.
func New(ctx context.Context, cfg config.Config) (Auth, error) {
	keyBytes, err := loadPrivateKey(cfg.GitHub.PrivateKey)
	if err != nil {
		return Auth{}, err
	}
	tr, err := ghinstallation.New(http.DefaultTransport, cfg.GitHub.AppID, cfg.GitHub.InstallationID, keyBytes)
	if err != nil {
		return Auth{}, fmt.Errorf("parse app private key: %w", err)
	}
	return Auth{
		HTTPClient: &http.Client{Transport: tr},
		GitToken:   tr.Token, // (*ghinstallation.Transport).Token(ctx) (string, error)
	}, nil
}

// HTTPClient is a convenience for API-only callers (provision, card): it returns
// New(ctx, cfg).HTTPClient.
func HTTPClient(ctx context.Context, cfg config.Config) (*http.Client, error) {
	a, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return a.HTTPClient, nil
}

// loadPrivateKey resolves the configured private key to PEM bytes, auto-detecting
// the form: an existing file path is read; a base64-encoded PEM is decoded; any
// other value is treated as raw PEM bytes. ghinstallation.New validates parseability.
func loadPrivateKey(v string) ([]byte, error) {
	if v == "" {
		return nil, fmt.Errorf("github.private_key is empty (set WAZIR_GITHUB_PRIVATE_KEY)")
	}
	if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
		return os.ReadFile(v)
	}
	if decoded, err := base64.StdEncoding.DecodeString(v); err == nil && bytes.Contains(decoded, []byte("PRIVATE KEY")) {
		return decoded, nil
	}
	return []byte(v), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/githubauth/ -v`
Expected: PASS (4 tests). `go build ./...` still clean (config still has the old `Auth`/`PAT` fields, which nothing in `githubauth` reads anymore; `serve` still uses `githubauth.HTTPClient` + `cfg.GitHub.PAT`, both still present).

- [ ] **Step 5: Commit**

```bash
git add internal/githubauth/auth.go internal/githubauth/auth_test.go
git commit -m "feat(githubauth): App-only Auth bundle (HTTPClient + git token source); drop PAT path"
```

---

## Task 3: Forge git token source + `serve` wiring (coupled — removes `Options.Token`)

**Files:**
- Modify: `internal/forge/github/forge.go`, `internal/forge/github/git.go`, `cmd/wazir/serve.go`
- Modify: `internal/forge/github/integration_test.go`
- Test: `internal/forge/github/git_test.go`, `internal/forge/github/forge_git_test.go`

- [ ] **Step 1: Write the failing runner tests**

Append to `internal/forge/github/git_test.go` (and add `context`, `fmt`, `os/exec` to its imports):

```go
func TestRunResolvesTokenForNetworkOps(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	called := 0
	g := gitRunner{bin: "git", token: func(context.Context) (string, error) { called++; return "tok", nil }}
	if _, err := g.run(context.Background(), "", true, "--version"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 1 {
		t.Errorf("token source called %d times, want 1", called)
	}
}

func TestRunSkipsTokenForNonNetworkOps(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g := gitRunner{bin: "git", token: func(context.Context) (string, error) {
		t.Fatal("token source must not be called for a non-network op")
		return "", nil
	}}
	if _, err := g.run(context.Background(), "", false, "--version"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRunPropagatesTokenError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	g := gitRunner{bin: "git", token: func(context.Context) (string, error) { return "", fmt.Errorf("mint failed") }}
	if _, err := g.run(context.Background(), "", true, "--version"); err == nil || !strings.Contains(err.Error(), "mint failed") {
		t.Fatalf("want the mint error surfaced, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/forge/github/ -run 'TestRun(Resolves|Skips|Propagates)' -v`
Expected: compile error (`gitRunner.token` is a `string`, not a func).

- [ ] **Step 3: Make `gitRunner.token` a token source**

In `internal/forge/github/git.go`, change the struct and env helpers. Replace the `gitRunner` type, `curatedGitEnv`, and `run` (keep `authConfigEnv` exactly as-is):

```go
// gitRunner execs `git` with a curated env. The token is injected as an
// http.extraHeader via GIT_CONFIG_* (not argv, so it never shows in `ps`, and
// not .git/config, so it never persists). Auth is added only for network ops,
// and the token is resolved per op so a refreshed installation token is used.
type gitRunner struct {
	bin   string
	token func(ctx context.Context) (string, error)
}

// curatedGitEnv is a minimal, secret-free base env for git children: PATH/HOME
// for resolution and GIT_TERMINAL_PROMPT=0 so a missing credential fails instead
// of hanging. The auth header (when needed) is appended by run(). WAZIR_* never leaks.
func curatedGitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT=0",
	}
}

// run execs `git args...`. dir sets cmd.Dir when non-empty. auth toggles the
// credential header (a fresh token is resolved from the token source for the op).
// It fails loudly with stderr on a non-zero exit.
func (g gitRunner) run(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := curatedGitEnv()
	if auth && g.token != nil {
		tok, err := g.token(ctx)
		if err != nil {
			return "", fmt.Errorf("get git token: %w", err)
		}
		env = append(env, authConfigEnv(tok)...)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
```

(The `git.go` imports — `bytes`, `context`, `encoding/base64`, `fmt`, `os`, `os/exec`, `strings` — are all still used.)

- [ ] **Step 4: Swap `Options.Token` for `Options.GitToken`**

In `internal/forge/github/forge.go`, change the `Options` field (line ~22) and the `New` wiring (line ~49):

```go
	Base         string
	GitToken     func(ctx context.Context) (string, error) // installation token per network op; nil = no auth header
	RemoteURL    func(repo string) string // optional; defaults to https://github.com/<repo>.git
```

```go
		git:          gitRunner{bin: opts.GitBin, token: opts.GitToken},
```

Also update the `Token`-mentioning doc comment near `resetOrigin`/`PushBranch` (the “PAT-bearing request” comment) to read “token-bearing request”. `forge.go` already imports `context`.

- [ ] **Step 5: Run the runner tests to verify they pass**

Run: `go test ./internal/forge/github/ -run 'TestRun(Resolves|Skips|Propagates)' -v`
Expected: PASS.

- [ ] **Step 6: Fix the forge unit-test construction sites**

In `internal/forge/github/forge_git_test.go`:

`newTestForge` (drop the `Token: ""`):
```go
	return New(nil, Options{
		GitBin: "git", Base: "main",
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
		RemoteURL: func(repo string) string { return origin },
	})
```

`TestForgeCloneDoesNotPersistToken` (the `Token: secret` site → a token source):
```go
	f := New(nil, Options{
		GitBin: "git", Base: "main",
		GitToken:  func(context.Context) (string, error) { return secret, nil },
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
		RemoteURL: func(repo string) string { return origin },
	})
```
(`forge_git_test.go` already imports `context`.)

- [ ] **Step 7: Update the forge integration test to App auth**

Overwrite `internal/forge/github/integration_test.go` with:

```go
//go:build integration

package github

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
)

// TestIntegrationForgeRoundTrip clones a real repo, creates a worktree, commits,
// pushes, and removes the worktree, authenticating with a GitHub App installation
// token. Requires (the App must be installed on the repo's account):
//
//	WAZIR_GITHUB_APP_ID, WAZIR_GITHUB_INSTALLATION_ID, WAZIR_GITHUB_PRIVATE_KEY (path or PEM),
//	WAZIR_IT_REPO (owner/name you can push to).
//
// Skips when unset. Run:
//
//	WAZIR_GITHUB_APP_ID=... WAZIR_GITHUB_INSTALLATION_ID=... WAZIR_GITHUB_PRIVATE_KEY=/path/key.pem \
//	WAZIR_IT_REPO=you/scratch \
//	go test -tags integration ./internal/forge/github/ -run TestIntegrationForgeRoundTrip -v
func TestIntegrationForgeRoundTrip(t *testing.T) {
	repo := os.Getenv("WAZIR_IT_REPO")
	if os.Getenv("WAZIR_GITHUB_APP_ID") == "" || repo == "" {
		t.Skip("set WAZIR_GITHUB_APP_ID/INSTALLATION_ID/PRIVATE_KEY and WAZIR_IT_REPO to run")
	}
	ctx := context.Background()
	appID, _ := strconv.ParseInt(os.Getenv("WAZIR_GITHUB_APP_ID"), 10, 64)
	instID, _ := strconv.ParseInt(os.Getenv("WAZIR_GITHUB_INSTALLATION_ID"), 10, 64)
	auth, err := githubauth.New(ctx, config.Config{GitHub: config.GitHubConfig{
		AppID: appID, InstallationID: instID, PrivateKey: os.Getenv("WAZIR_GITHUB_PRIVATE_KEY"),
	}})
	if err != nil {
		t.Fatalf("githubauth.New: %v", err)
	}
	f := New(nil, Options{
		GitBin: "git", Base: "main", GitToken: auth.GitToken,
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
	})
	if _, err := f.EnsureClone(ctx, repo); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	branch := "wazir-it/" + t.Name()
	wt, err := f.CreateWorktree(ctx, repo, branch)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.WriteFile(wt+"/WAZIR_IT.txt", []byte("integration\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-c", "user.email=it@wazir", "-c", "user.name=Wazir IT", "add", "-A"},
		{"-c", "user.email=it@wazir", "-c", "user.name=Wazir IT", "commit", "-m", "wazir integration commit"},
	} {
		if _, err := f.git.run(ctx, wt, false, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if err := f.PushBranch(ctx, repo, branch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	t.Logf("pushed %s to %s — delete the branch when done", branch, repo)
	if err := f.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
}
```

Verify it compiles under the build tag:
Run: `go vet -tags integration ./internal/forge/github/`
Expected: clean.

- [ ] **Step 8: Wire `serve` to one `githubauth.New` + eager-mint**

In `cmd/wazir/serve.go`, replace the auth + board + forge construction. Change the block that currently reads:

```go
	hc, err := githubauth.HTTPClient(ctx, cfg)
	if err != nil {
		return err
	}
```
to:
```go
	auth, err := githubauth.New(ctx, cfg)
	if err != nil {
		return err
	}
	// Fail loud at startup if the App credentials can't mint an installation
	// token, rather than discovering it mid-turn (mirrors the plugin-dir resolve).
	if _, err := auth.GitToken(ctx); err != nil {
		return fmt.Errorf("mint installation token (check app_id/installation_id/private_key): %w", err)
	}
```

Change the board construction `b := boardgh.New(hc, cfg, st)` to:
```go
	b := boardgh.New(auth.HTTPClient, cfg, st)
```

Change the forge construction to use the API client + git token source (drop `Token:`):
```go
	f := forgegh.New(github.NewClient(auth.HTTPClient), forgegh.Options{
		GitBin:       cfg.Forge.GitBin,
		CloneRoot:    cfg.Forge.CloneRoot,
		WorktreeRoot: cfg.Forge.WorktreeRoot,
		Base:         cfg.Forge.BaseBranch,
		GitToken:     auth.GitToken,
	})
```

- [ ] **Step 9: Verify build + the forge package tests**

Run: `go build ./... && go test ./internal/forge/... ./internal/githubauth/...`
Expected: build clean; PASS. (`serve` no longer references `cfg.GitHub.PAT`; `provision`/`card` still use the `githubauth.HTTPClient` wrapper and are unchanged.)

- [ ] **Step 10: Commit**

```bash
git add internal/forge/github/ cmd/wazir/serve.go
git commit -m "feat(forge,serve): git token source from the App transport; eager-mint at startup"
```

---

## Task 4: Config — remove `Auth`/`PAT`, require App fields, `owner_type=org`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`, `internal/config/config_number_test.go`
- Modify: `internal/board/github/integration_test.go` (doc comment)

- [ ] **Step 1: Update `GitHubConfig` and `validate`**

In `internal/config/config.go`, replace the `GitHubConfig` struct (lines ~55-63) with:

```go
// GitHubConfig holds GitHub App auth + GitHub-side identity. Secrets
// (webhook_secret, private_key) are normally supplied via env, not the file.
type GitHubConfig struct {
	OwnerType      string `fig:"owner_type" default:"org"` // must be org — an App can't drive a user-owned Projects v2 board
	WebhookSecret  string `fig:"webhook_secret"`           // WAZIR_GITHUB_WEBHOOK_SECRET
	AppID          int64  `fig:"app_id"`                   // WAZIR_GITHUB_APP_ID
	InstallationID int64  `fig:"installation_id"`          // WAZIR_GITHUB_INSTALLATION_ID
	PrivateKey     string `fig:"private_key"`              // WAZIR_GITHUB_PRIVATE_KEY — .pem path or PEM bytes (base64-aware)
}
```

Replace the `validate()` body's GitHub-auth section (lines ~111-125) with:

```go
func (c Config) validate() error {
	if c.GitHub.AppID == 0 || c.GitHub.InstallationID == 0 || c.GitHub.PrivateKey == "" {
		return fmt.Errorf("github app auth requires github.app_id, github.installation_id, and github.private_key " +
			"(set WAZIR_GITHUB_APP_ID / WAZIR_GITHUB_INSTALLATION_ID / WAZIR_GITHUB_PRIVATE_KEY)")
	}
	if c.GitHub.OwnerType != "org" {
		return fmt.Errorf("github.owner_type must be org for GitHub App auth "+
			"(a user-owned Projects v2 board is not accessible to an App), got %q", c.GitHub.OwnerType)
	}
	if c.Project.Owner == "" {
		return fmt.Errorf("project.owner is required")
	}
	if c.Project.Number <= 0 {
		return fmt.Errorf("project.number must be set and > 0 (got %d)", c.Project.Number)
	}
	return nil
}
```

Update the stale comment at line ~16 (`// github.pat field is overridden by WAZIR_GITHUB_PAT.`) to:
```go
// e.g. the github.app_id field is overridden by WAZIR_GITHUB_APP_ID.
```

- [ ] **Step 2: Add the App-env test helper and fix the config tests**

In `internal/config/config_test.go`, add this helper near the top (after the imports):

```go
// setAppEnv sets the GitHub App auth env so Load() passes validation. owner_type
// defaults to org, so it is not set here.
func setAppEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WAZIR_GITHUB_APP_ID", "111")
	t.Setenv("WAZIR_GITHUB_INSTALLATION_ID", "222")
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "/tmp/wazir-test-key.pem")
}
```

Replace `sampleYAML`'s `github:` section (lines ~12-15) with:
```yaml
github:
  app_id: 111
  installation_id: 222
  private_key: filekey
  owner_type: org
```

In `TestLoadFromFile`, replace the auth assertion (line ~43):
```go
	if c.GitHub.AppID != 111 || c.GitHub.InstallationID != 222 || c.GitHub.PrivateKey != "filekey" {
		t.Errorf("github = %+v", c.GitHub)
	}
```

Replace `TestEnvOverridesFile` body (lines ~58-70) so it overrides a field that still exists:
```go
	t.Setenv("WAZIR_GITHUB_PRIVATE_KEY", "envkey")
	t.Setenv("WAZIR_PROJECT_NUMBER", "99")

	c, err := Load(writeConfig(t, sampleYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.PrivateKey != "envkey" {
		t.Errorf("env should override file private_key, got %q", c.GitHub.PrivateKey)
	}
	if c.Project.Number != 99 {
		t.Errorf("env should override file number, got %d", c.Project.Number)
	}
```

Replace `TestLoadEnvOnlyWithDefaults` (lines ~73-98) with:
```go
func TestLoadEnvOnlyWithDefaults(t *testing.T) {
	t.Chdir(t.TempDir()) // ensure no ./wazir.yaml is discovered
	setAppEnv(t)
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")

	c, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub.OwnerType != "org" {
		t.Errorf("owner_type default = %q, want org", c.GitHub.OwnerType)
	}
	if c.Project.BoardName != "Wazir" {
		t.Errorf("board_name default = %q, want Wazir", c.Project.BoardName)
	}
	if c.Store.DBPath != "wazir.db" {
		t.Errorf("db_path default = %q, want wazir.db", c.Store.DBPath)
	}
	if c.Project.Owner != "octocat" || c.Project.Number != 5 {
		t.Errorf("project from env = %+v", c.Project)
	}
}
```

Replace `TestLoadRejectsPATAuthWithoutToken` (lines ~100-107) with two tests:
```go
func TestLoadRejectsMissingAppFields(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")
	// no app_id/installation_id/private_key
	if _, err := Load(""); err == nil {
		t.Fatal("expected error when the App auth fields are missing")
	}
}

func TestLoadRejectsUserOwnerType(t *testing.T) {
	t.Chdir(t.TempDir())
	setAppEnv(t)
	t.Setenv("WAZIR_GITHUB_OWNER_TYPE", "user")
	t.Setenv("WAZIR_PROJECT_OWNER", "octocat")
	t.Setenv("WAZIR_PROJECT_NUMBER", "5")
	if _, err := Load(""); err == nil {
		t.Fatal("expected error: an App can't drive a user-owned board")
	}
}
```

In each of `TestClaudeDefaults`, `TestClaudeEnvOverrides`, `TestForgeAndClaudeM4Defaults`, `TestForgeEnvOverrides`, and `TestClaudeIsolationConfig`, replace the single line `t.Setenv("WAZIR_GITHUB_PAT", "x")` with `setAppEnv(t)`. (Leave the `WAZIR_PROJECT_OWNER` / `WAZIR_PROJECT_NUMBER` lines.)

- [ ] **Step 3: Fix `config_number_test.go`**

In `internal/config/config_number_test.go`, replace `t.Setenv("WAZIR_GITHUB_PAT", "tok")` with `setAppEnv(t)`.

- [ ] **Step 4: Update the board integration-test doc comment**

In `internal/board/github/integration_test.go`, replace the run-command comment (lines ~16-20) with:
```go
// Run with (env-only config, no file needed). The App must be installed on the org:
//
//	WAZIR_GITHUB_APP_ID=... WAZIR_GITHUB_INSTALLATION_ID=... WAZIR_GITHUB_PRIVATE_KEY=/path/key.pem \
//	WAZIR_GITHUB_OWNER_TYPE=org WAZIR_PROJECT_OWNER=your-org WAZIR_PROJECT_NUMBER=NN \
//	go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
```
(The body — `githubauth.HTTPClient(context.Background(), cfg)` — is unchanged.)

- [ ] **Step 5: Run the config + full unit suite**

Run: `go test ./internal/config/... && go build ./... && go test ./...`
Expected: PASS across the board; no remaining `WAZIR_GITHUB_PAT` / `cfg.GitHub.Auth` / `cfg.GitHub.PAT` references compile.

- [ ] **Step 6: Verify the integration tests still compile**

Run: `go vet -tags integration ./...`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/config/ internal/board/github/integration_test.go
git commit -m "feat(config): App-only auth — require app_id/installation_id/private_key, owner_type=org; drop PAT"
```

---

## Task 5: Docs — config template + CLAUDE.md

**Files:** `wazir.example.yaml`, `CLAUDE.md`

- [ ] **Step 1: Update `wazir.example.yaml`**

Replace the `github:` block and the PAT env hint. The `github:` section should read:
```yaml
github:
  # GitHub App auth (the only supported mode). Secrets come from env, not this file:
  #   export WAZIR_GITHUB_APP_ID=...
  #   export WAZIR_GITHUB_INSTALLATION_ID=...
  #   export WAZIR_GITHUB_PRIVATE_KEY=/path/to/app.private-key.pem   # .pem path OR PEM bytes (base64 ok)
  #   export WAZIR_GITHUB_WEBHOOK_SECRET=...
  owner_type: org     # must be org — an App can't access a user-owned Projects v2 board
  # app_id: 0
  # installation_id: 0
  # private_key: ""    # prefer WAZIR_GITHUB_PRIVATE_KEY
```
Remove any `auth:` / `pat:` lines and the `# export WAZIR_GITHUB_PAT=...` comment.

- [ ] **Step 2: Update `CLAUDE.md`**

Make three edits:
- The integration-test command (line ~52): replace the `WAZIR_GITHUB_PAT=$(gh auth token) WAZIR_GITHUB_OWNER_TYPE=user \` line with:
  ```
  WAZIR_GITHUB_APP_ID=… WAZIR_GITHUB_INSTALLATION_ID=… WAZIR_GITHUB_PRIVATE_KEY=/path/key.pem WAZIR_GITHUB_OWNER_TYPE=org \
  ```
- The auth-seam description (line ~118): replace `PAT ships; GitHub App is scaffolded behind \`auth: app\` (\`ErrAppAuthNotWired\`).` with: `Auth is GitHub App only — one \`ghinstallation.Transport\` backs the REST + GraphQL client and a git token source; PAT support was removed in M5 slice 2.`
- The config env examples (line ~124): replace `WAZIR_GITHUB_PAT` with `WAZIR_GITHUB_APP_ID` in the list.
- The `## Status` paragraph: add a sentence that M5 slice 2 (GitHub App token auth, App-only) is in progress, referencing `docs/superpowers/specs/2026-06-13-wazir-m5-app-auth-design.md` and this plan.

- [ ] **Step 3: Verify nothing broke**

Run: `go build ./...`
Expected: clean (docs-only).

- [ ] **Step 4: Commit**

```bash
git add wazir.example.yaml CLAUDE.md
git commit -m "docs(m5): document GitHub App auth config + status; remove PAT references"
```

---

## Task 6: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build, test, vet, race**

Run:
```bash
go build ./...
go test ./...
go vet ./...
go test -race ./...
```
Expected: all green, no network/credentials needed.

- [ ] **Step 2: Confirm the dependency rule still holds**

Run: `go test ./internal/orchestrator/ -run TestImports -v`
Expected: PASS — the core imports no provider package.

- [ ] **Step 3: Confirm the integration tests compile**

Run: `go vet -tags integration ./...`
Expected: clean (the build-tagged `board/github` + `forge/github` live tests compile against the new App-auth surface).

- [ ] **Step 4: Grep for stragglers**

Run: `grep -rn -e "GitHub.PAT" -e "cfg.GitHub.Auth" -e "ErrAppAuthNotWired" -e "bearerTransport" --include="*.go" .`
Expected: no matches (only docs/superpowers history may still mention them).

---

## Self-review notes (author)

- **Spec coverage:** §3 seam → Task 2; §5 githubauth + key auto-detect → Task 2; §6 forge token source → Task 3; §8 serve wiring + eager-mint → Task 3; §7 config (drop PAT, require App fields, owner_type=org) → Task 4; §11 testing → woven through + Task 6; PAT removal across tests/docs → Tasks 2-5; §13 operational prerequisites → documented in the spec (no code). New dependency (§2) → Task 1.
- **Type consistency:** `Auth{HTTPClient *http.Client, GitToken func(ctx) (string, error)}`, `loadPrivateKey(string) ([]byte, error)`, `Options.GitToken`/`gitRunner.token` as `func(ctx context.Context) (string, error)`, and `ghinstallation.New(http.RoundTripper, int64, int64, []byte)` / `(*Transport).Token(ctx)` are used identically across tasks.
- **Compile-stability:** Task 2 leaves `Auth`/`PAT` config fields unused-but-present; Task 3 removes `serve`'s `cfg.GitHub.PAT` use; Task 4 deletes the fields with no dangling references. Each task ends green.
- **Out of scope (spec §12):** multi-account/multi-installation routing, label-based approval, retry/backoff on transient mint failures, and the org migration itself.
```
