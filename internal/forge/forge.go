// Package forge defines the CodeForge port (clone/worktree/branch/PR).
package forge

import (
	"context"
	"errors"
)

// ErrNotImplemented marks methods delivered in a later milestone (M4).
var ErrNotImplemented = errors.New("forge: not implemented in M0")

// CodeForge is the VCS surface. M0 implements only OpenPR.
type CodeForge interface {
	Clone(ctx context.Context, repo, dest string) error
	CreateWorktree(ctx context.Context, repo, branch, path string) error
	RemoveWorktree(ctx context.Context, path string) error
	PushBranch(ctx context.Context, repo, branch string) error
	OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, err error)
}
