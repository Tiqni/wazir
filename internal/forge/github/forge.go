// Package github implements the forge.CodeForge port.
package github

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/forge"
)

// Options configures the local git layout + auth for the GitHub forge.
type Options struct {
	GitBin       string
	CloneRoot    string
	WorktreeRoot string
	Base         string
	GitToken     func(ctx context.Context) (string, error) // installation token per network op; nil = no auth header
	RemoteURL    func(repo string) string                  // optional; defaults to https://github.com/<repo>.git
}

// GitHubForge implements forge.CodeForge.
type GitHubForge struct {
	rest         *github.Client
	git          gitRunner
	cloneRoot    string
	worktreeRoot string
	base         string
	remoteURL    func(repo string) string
}

// New returns a GitHubForge. rest may be nil in git-only tests.
func New(rest *github.Client, opts Options) *GitHubForge {
	if opts.GitBin == "" {
		opts.GitBin = "git"
	}
	if opts.Base == "" {
		opts.Base = "main"
	}
	if opts.RemoteURL == nil {
		opts.RemoteURL = func(repo string) string { return "https://github.com/" + repo + ".git" }
	}
	return &GitHubForge{
		rest:         rest,
		git:          gitRunner{bin: opts.GitBin, token: opts.GitToken},
		cloneRoot:    opts.CloneRoot,
		worktreeRoot: opts.WorktreeRoot,
		base:         opts.Base,
		remoteURL:    opts.RemoteURL,
	}
}

func splitRepo(full string) (owner, name string, err error) {
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q (want owner/name)", full)
	}
	return parts[0], parts[1], nil
}

func (f *GitHubForge) clonePath(repo string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(f.cloneRoot, owner, name), nil
}

func (f *GitHubForge) worktreePath(repo, branch string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	slug := strings.ReplaceAll(branch, "/", "-")
	return filepath.Join(f.worktreeRoot, owner+"-"+name+"-"+slug), nil
}

// EnsureClone makes the clone present + current (clone if absent, else fetch) and returns its path.
func (f *GitHubForge) EnsureClone(ctx context.Context, repo string) (string, error) {
	clone, err := f.clonePath(repo)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(filepath.Join(clone, ".git")); statErr == nil {
		if err := f.resetOrigin(ctx, clone, repo); err != nil {
			return "", err
		}
		if _, err := f.git.run(ctx, clone, true, "fetch", "origin", "--prune"); err != nil {
			return "", err
		}
		return clone, nil
	}
	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		return "", fmt.Errorf("mkdir clone parent: %w", err)
	}
	if _, err := f.git.run(ctx, "", true, "clone", f.remoteURL(repo), clone); err != nil {
		return "", err
	}
	return clone, nil
}

// CreateWorktree adds a worktree on `branch`, reset to origin/<base>, and returns its path.
func (f *GitHubForge) CreateWorktree(ctx context.Context, repo, branch string) (string, error) {
	clone, err := f.clonePath(repo)
	if err != nil {
		return "", err
	}
	wt, err := f.worktreePath(repo, branch)
	if err != nil {
		return "", err
	}
	// Idempotent re-entry: drop a stale worktree at the same path, then prune.
	_, _ = f.git.run(ctx, clone, false, "worktree", "remove", "--force", wt)
	_, _ = f.git.run(ctx, clone, false, "worktree", "prune")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return "", fmt.Errorf("mkdir worktree parent: %w", err)
	}
	// -B creates or resets the branch to the base; the worker re-plans from scratch on re-entry.
	if _, err := f.git.run(ctx, clone, false, "worktree", "add", "-B", branch, wt, "origin/"+f.base); err != nil {
		return "", err
	}
	return wt, nil
}

// RemoveWorktree removes the worktree at path (run from the clone) and prunes.
func (f *GitHubForge) RemoveWorktree(ctx context.Context, repo, path string) error {
	clone, err := f.clonePath(repo)
	if err != nil {
		return err
	}
	if _, err := f.git.run(ctx, clone, false, "worktree", "remove", "--force", path); err != nil {
		return err
	}
	_, _ = f.git.run(ctx, clone, false, "worktree", "prune")
	return nil
}

// PushBranch pushes branch (created in a linked worktree, ref lives in the shared clone) to origin.
func (f *GitHubForge) PushBranch(ctx context.Context, repo, branch string) error {
	clone, err := f.clonePath(repo)
	if err != nil {
		return err
	}
	if err := f.resetOrigin(ctx, clone, repo); err != nil {
		return err
	}
	_, err = f.git.run(ctx, clone, true, "push", "origin", branch)
	return err
}

// resetOrigin points the clone's origin remote at the canonical URL before any
// authenticated network op. A worktree's execute turn has git access and shares
// this clone's config, so a tampered origin could otherwise redirect the
// token-bearing request (http.extraHeader) to another host — an exfiltration vector
// under the §12 prompt-injection threat model. It's a local op (no auth).
func (f *GitHubForge) resetOrigin(ctx context.Context, clone, repo string) error {
	if _, err := f.git.run(ctx, clone, false, "remote", "set-url", "origin", f.remoteURL(repo)); err != nil {
		return fmt.Errorf("reset origin url: %w", err)
	}
	return nil
}

// OpenPR opens a pull request and returns its HTML URL.
func (f *GitHubForge) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	pr, _, err := f.rest.PullRequests.Create(ctx, owner, name, &github.NewPullRequest{
		Title: &title,
		Head:  &branch,
		Base:  &base,
		Body:  &body,
	})
	if err != nil {
		return "", fmt.Errorf("create pr: %w", err)
	}
	return pr.GetHTMLURL(), nil
}

var _ forge.CodeForge = (*GitHubForge)(nil)
