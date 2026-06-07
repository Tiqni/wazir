// Package forge defines the CodeForge port (clone/worktree/branch/PR).
package forge

import (
	"context"
	"errors"
)

// ErrNotImplemented marks forge methods not yet wired for the active provider.
var ErrNotImplemented = errors.New("forge: not implemented")

// CodeForge is the VCS surface. The forge owns filesystem layout (clone +
// worktree roots) so the provider-free core never holds local paths.
type CodeForge interface {
	// EnsureClone makes the local clone for repo present and current
	// (clone if absent, else fetch). Idempotent.
	EnsureClone(ctx context.Context, repo string) error
	// CreateWorktree adds a worktree on a fresh branch (reset to base) and
	// returns its absolute path.
	CreateWorktree(ctx context.Context, repo, branch string) (path string, err error)
	// RemoveWorktree removes the worktree at path (repo locates the clone).
	RemoveWorktree(ctx context.Context, repo, path string) error
	// PushBranch pushes branch to origin.
	PushBranch(ctx context.Context, repo, branch string) error
	// OpenPR opens a pull request and returns its URL.
	OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, err error)
}
