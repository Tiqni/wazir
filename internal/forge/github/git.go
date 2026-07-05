package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/EmadMokhtar/wazir/internal/retry"
)

// gitRunner execs `git` with a curated env. The token is injected as an
// http.extraHeader via GIT_CONFIG_* (not argv, so it never shows in `ps`, and
// not .git/config, so it never persists). Auth is added only for network ops,
// and the token is resolved per op so a refreshed installation token is used.
type gitRunner struct {
	bin    string
	token  func(ctx context.Context) (string, error)
	policy retry.Policy // applied to network ops only
}

// authConfigEnv returns the GIT_CONFIG_* env that injects an Authorization
// header for HTTPS git, or nil when token is empty.
func authConfigEnv(token string) []string {
	if token == "" {
		return nil
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=AUTHORIZATION: basic " + b64,
	}
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
// For network ops (auth == true) it retries transient failures (host
// resolution, timeouts, remote 5xx) with bounded backoff; local ops run
// exactly once. It fails loudly with stderr on a non-zero exit.
func (g gitRunner) run(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
	if !auth {
		return g.runOnce(ctx, dir, auth, args...)
	}
	var out string
	err := retry.Do(ctx, g.policy,
		func(err error) (bool, time.Duration) { return transientGit(err), 0 },
		func() error {
			var e error
			out, e = g.runOnce(ctx, dir, auth, args...)
			return e
		})
	return out, err
}

// runOnce is a single git invocation (the former body of run).
func (g gitRunner) runOnce(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
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

// transientGit reports whether a git error looks like a retryable network
// failure (stderr matched case-insensitively). It deliberately excludes logic
// failures — merge conflicts, non-fast-forward pushes, auth, "nothing to commit".
func transientGit(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, p := range []string{
		"could not resolve host",
		"connection timed out",
		"connection reset",
		"connection refused",
		"operation timed out",
		"early eof",
		"the remote end hung up",
		"rpc failed",
		"unable to access", // git's curl wrapper for HTTP transport trouble
		"failed to connect",
		"gnutls_handshake",
		"ssl_",
		"tls",
		"error: 500", "error: 502", "error: 503", "error: 504",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
