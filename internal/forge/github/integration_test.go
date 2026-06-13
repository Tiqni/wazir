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
	if os.Getenv("WAZIR_GITHUB_APP_ID") == "" || os.Getenv("WAZIR_GITHUB_INSTALLATION_ID") == "" || repo == "" {
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
