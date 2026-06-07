package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gitRunner execs `git` with a curated env. The PAT is injected as an
// http.extraHeader via GIT_CONFIG_* (not argv, so it never shows in `ps`, and
// not .git/config, so it never persists). Auth is added only for network ops.
type gitRunner struct {
	bin   string
	token string
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

// curatedGitEnv is a minimal, secret-free env for git children: PATH/HOME for
// resolution, GIT_TERMINAL_PROMPT=0 so a missing credential fails instead of
// hanging, plus the auth header when this is a network op. WAZIR_* never leaks.
func curatedGitEnv(auth bool, token string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT=0",
	}
	if auth {
		env = append(env, authConfigEnv(token)...)
	}
	return env
}

// run execs `git args...`. dir sets cmd.Dir when non-empty. auth toggles the
// credential header. It fails loudly with stderr on a non-zero exit.
func (g gitRunner) run(ctx context.Context, dir string, auth bool, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = curatedGitEnv(auth, g.token)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (stderr: %s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
