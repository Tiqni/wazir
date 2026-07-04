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

// ReviewFeedback is the changes-requested review body + line-level comments.
type ReviewFeedback struct {
	Summary  string          // the most recent changes-requested review body
	Comments []InlineComment // line-level review comments
}

// InlineComment is one line-level PR review comment.
type InlineComment struct {
	Path   string
	Line   int
	Body   string
	Author string
}

// CheckAnnotation is one annotation of a failed check-run.
type CheckAnnotation struct {
	Check   string // check-run name
	Path    string
	Line    int
	Level   string // "failure" | "warning" | "notice"
	Message string
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
	// OpenPR opens a pull request and returns its URL and number.
	OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, prNumber int, err error)
	// PRStatus reports the current review decision + CI conclusion for a PR.
	PRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error)
	// CreateWorktreeFromBranch adds a worktree at the branch's REMOTE head
	// (origin/<branch>, after a fetch) — preserves the PR's commits. The rework
	// mirror of CreateWorktree (which resets to base).
	CreateWorktreeFromBranch(ctx context.Context, repo, branch string) (path string, err error)
	// PRReviewFeedback returns the changes-requested review body + inline comments.
	PRReviewFeedback(ctx context.Context, repo string, prNumber int) (ReviewFeedback, error)
	// CheckAnnotations returns the annotations of the PR head's failed check-runs.
	CheckAnnotations(ctx context.Context, repo string, prNumber int) ([]CheckAnnotation, error)
}
