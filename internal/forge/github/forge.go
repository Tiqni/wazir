// Package github implements the forge.CodeForge port. M0 ships OpenPR;
// clone/worktree/push land in M4.
package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/forge"
)

// GitHubForge implements forge.CodeForge.
type GitHubForge struct {
	rest *github.Client
}

// New returns a GitHubForge over an authenticated REST client.
func New(rest *github.Client) *GitHubForge { return &GitHubForge{rest: rest} }

func splitRepo(full string) (owner, name string, err error) {
	parts := strings.Split(full, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q (want owner/name)", full)
	}
	return parts[0], parts[1], nil
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

func (f *GitHubForge) EnsureClone(ctx context.Context, repo string) error { return forge.ErrNotImplemented }
func (f *GitHubForge) CreateWorktree(ctx context.Context, repo, branch string) (string, error) {
	return "", forge.ErrNotImplemented
}
func (f *GitHubForge) RemoveWorktree(ctx context.Context, repo, path string) error { return forge.ErrNotImplemented }
func (f *GitHubForge) PushBranch(ctx context.Context, repo, branch string) error   { return forge.ErrNotImplemented }

var _ forge.CodeForge = (*GitHubForge)(nil)
