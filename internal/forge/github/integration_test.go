//go:build integration

package github

import (
	"context"
	"os"
	"testing"
)

// TestIntegrationForgeRoundTrip clones a real repo, creates a worktree, makes a
// trivial commit, pushes, and removes the worktree. Requires:
//
//	WAZIR_GITHUB_PAT, WAZIR_IT_REPO (owner/name you can push to).
//
// Skips when unset. Run:
//
//	WAZIR_GITHUB_PAT=$(gh auth token) WAZIR_IT_REPO=you/scratch \
//	go test -tags integration ./internal/forge/github/ -run TestIntegrationForgeRoundTrip -v
func TestIntegrationForgeRoundTrip(t *testing.T) {
	pat := os.Getenv("WAZIR_GITHUB_PAT")
	repo := os.Getenv("WAZIR_IT_REPO")
	if pat == "" || repo == "" {
		t.Skip("set WAZIR_GITHUB_PAT and WAZIR_IT_REPO to run")
	}
	ctx := context.Background()
	f := New(nil, Options{
		GitBin: "git", Base: "main", Token: pat,
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
	})
	if err := f.EnsureClone(ctx, repo); err != nil {
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
