package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedBareOrigin creates a bare repo with one commit on `main` and returns its path.
func seedBareOrigin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "--bare", "-b", "main", origin)
	seed := filepath.Join(root, "seed")
	run(root, "clone", origin, seed)
	if err := os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(seed, "-c", "user.email=t@w", "-c", "user.name=W", "add", "-A")
	run(seed, "-c", "user.email=t@w", "-c", "user.name=W", "commit", "-m", "seed")
	run(seed, "branch", "-M", "main")
	run(seed, "push", "origin", "main")
	return origin
}

func newTestForge(t *testing.T, origin string) *GitHubForge {
	t.Helper()
	return New(nil, Options{
		GitBin: "git", Base: "main", Token: "",
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
		RemoteURL: func(repo string) string { return origin },
	})
}

func TestForgeCloneWorktreePushRemove(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	origin := seedBareOrigin(t)
	f := newTestForge(t, origin)
	const repo = "owner/name"

	if err := f.EnsureClone(ctx, repo); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	if err := f.EnsureClone(ctx, repo); err != nil {
		t.Fatalf("EnsureClone (idempotent fetch): %v", err)
	}
	wt, err := f.CreateWorktree(ctx, repo, "feature/issue-7-x")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "main.go")); err != nil {
		t.Errorf("worktree missing seeded file: %v", err)
	}
	commit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@w", "-c", "user.name=W"}, args...)...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("add", "-A")
	commit("commit", "-m", "work")

	if err := f.PushBranch(ctx, repo, "feature/issue-7-x"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	ls := exec.Command("git", "-C", origin, "rev-parse", "--verify", "feature/issue-7-x")
	if out, err := ls.CombinedOutput(); err != nil {
		t.Fatalf("branch not pushed to origin: %v\n%s", err, out)
	}
	if err := f.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after remove: err=%v", err)
	}
}

func TestForgeCloneDoesNotPersistToken(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	origin := seedBareOrigin(t)
	const secret = "super-secret-pat-DO-NOT-LEAK"
	f := New(nil, Options{
		GitBin: "git", Base: "main", Token: secret,
		CloneRoot: t.TempDir(), WorktreeRoot: t.TempDir(),
		RemoteURL: func(repo string) string { return origin },
	})
	const repo = "owner/name"
	if err := f.EnsureClone(ctx, repo); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	clone, err := f.clonePath(repo)
	if err != nil {
		t.Fatalf("clonePath: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(clone, ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfg), secret) {
		t.Errorf("PAT leaked into .git/config:\n%s", cfg)
	}
}
