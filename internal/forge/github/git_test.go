package github

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
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
