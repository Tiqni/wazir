// Package forge defines the CodeForge port (clone/worktree/branch/PR).
package forge

import (
	"context"
	"errors"
)

// ErrNotImplemented marks forge methods not yet wired for the active provider.
var ErrNotImplemented = errors.New("forge: not implemented")

// PRStatus is the observed review + CI state of a pull request. Values are
// domain tokens; no provider types cross this port.
type PRStatus struct {
	ReviewDecision string   // "approved" | "changes_requested" | "" (no decisive review)
	CIConclusion   string   // "success" | "failure" | "pending" | ""  ("" = no checks present)
	FailingChecks  []string // names of failed check-runs, for the report comment
	HeadSHA        string   // the commit the checks ran against
}

// CodeForge is the VCS surface. The forge owns filesystem layout (clone +
// worktree roots) so the provider-free core never holds local paths.
type CodeForge interface {
	// EnsureClone makes the local clone for repo present and current
	// (clone if absent, else fetch) and returns its absolute path. Idempotent.
	EnsureClone(ctx context.Context, repo string) (clonePath string, err error)
	// CreateWorktree adds a worktree on a fresh branch (reset to base) and
	// returns its absolute path.
	CreateWorktree(ctx context.Context, repo, branch string) (path string, err error)
	// RemoveWorktree removes the worktree at path (repo locates the clone).
	RemoveWorktree(ctx context.Context, repo, path string) error
	// PushBranch pushes branch to origin.
	PushBranch(ctx context.Context, repo, branch string) error
	// OpenPR opens a pull request and returns its URL.
	OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, err error)
	// PRStatus reports the current review decision + CI conclusion for a PR.
	PRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error)
}
