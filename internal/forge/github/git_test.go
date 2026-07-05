package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EmadMokhtar/wazir/internal/retry"
)

func TestAuthConfigEnvBuildsExtraHeader(t *testing.T) {
	env := authConfigEnv("ghp_secret")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.extraHeader") {
		t.Errorf("missing extraHeader key:\n%s", joined)
	}
	// base64("x-access-token:ghp_secret")
	if !strings.Contains(joined, "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic eC1hY2Nlc3MtdG9rZW46Z2hwX3NlY3JldA==") {
		t.Errorf("wrong/absent basic header:\n%s", joined)
	}
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=1") {
		t.Errorf("missing GIT_CONFIG_COUNT:\n%s", joined)
	}
}

func TestAuthConfigEnvEmptyTokenIsNil(t *testing.T) {
	if authConfigEnv("") != nil {
		t.Error("empty token must yield no auth env")
	}
}

func TestRunResolvesTokenForNetworkOps(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	called := 0
	g := gitRunner{bin: "git", token: func(context.Context) (string, error) { called++; return "tok", nil }}
	for i := range 2 {
		if _, err := g.run(context.Background(), "", true, "--version"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	// Resolved per network op (not cached in the runner): two ops → two calls.
	if called != 2 {
		t.Errorf("token source called %d times across 2 network ops, want 2", called)
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

// countingGit writes a fake `git` that fails transiently on its first N calls
// (printing a network-looking stderr, exit 1) then succeeds. It counts via a
// marker file so the count survives across processes.
func countingGit(t *testing.T, failFirst int) (bin string, countPath string) {
	t.Helper()
	dir := t.TempDir()
	countPath = filepath.Join(dir, "count")
	bin = filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"n=$(cat '" + countPath + "' 2>/dev/null || echo 0)\n" +
		"n=$((n+1)); echo $n > '" + countPath + "'\n" +
		"if [ \"$n\" -le " + fmt.Sprint(failFirst) + " ]; then\n" +
		"  echo \"fatal: unable to access 'https://x/': Could not resolve host: x\" >&2; exit 128\n" +
		"fi\n" +
		"echo ok\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, countPath
}

func TestTransientGitClassifier(t *testing.T) {
	yes := []string{
		"git push: exit 128 (stderr: fatal: unable to access 'https://x': Could not resolve host: x)",
		"stderr: Connection timed out",
		"stderr: the remote end hung up unexpectedly",
		"stderr: fatal: unable to access: The requested URL returned error: 503",
	}
	for _, s := range yes {
		if !transientGit(errors.New(s)) {
			t.Errorf("want transient: %q", s)
		}
	}
	no := []string{
		"stderr: CONFLICT (content): Merge conflict in a.go",
		"stderr: ! [rejected] main -> main (non-fast-forward)",
		"stderr: nothing to commit, working tree clean",
	}
	for _, s := range no {
		if transientGit(errors.New(s)) {
			t.Errorf("want NOT transient: %q", s)
		}
	}
	if transientGit(nil) {
		t.Error("nil must not be transient")
	}
}

func TestRunRetriesTransientNetworkOp(t *testing.T) {
	bin, _ := countingGit(t, 2) // fail twice, succeed on the 3rd
	g := gitRunner{
		bin:    bin,
		token:  func(context.Context) (string, error) { return "tok", nil },
		policy: retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
	}
	out, err := g.run(context.Background(), "", true, "push", "origin", "b")
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v, want ok/nil after retries", out, err)
	}
}

func TestRunDoesNotRetryLocalOp(t *testing.T) {
	bin, countPath := countingGit(t, 5) // always fails within the test's attempts
	g := gitRunner{
		bin:    bin,
		token:  func(context.Context) (string, error) { return "tok", nil },
		policy: retry.Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}
	if _, err := g.run(context.Background(), "", false, "worktree", "prune"); err == nil {
		t.Fatal("want an error; a local op must not retry")
	}
	if b, _ := os.ReadFile(countPath); strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("local op ran %s times, want exactly 1 (no retry)", strings.TrimSpace(string(b)))
	}
}
