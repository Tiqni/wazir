package orchestrator

import (
	"context"
	"errors"
)

// ErrPhaseRequiresWorktree marks a phase whose live execution needs an isolated
// git worktree, delivered in M4. The M2 ClaudeBrain returns it from Plan/Execute;
// the Worker recognizes it and defers gracefully (no Failed). It lives in this
// (port) package, not internal/claude, so the provider-free Worker can errors.Is
// it without importing a provider.
var ErrPhaseRequiresWorktree = errors.New("orchestrator: phase requires a worktree (delivered in M4)")

// BrainstormStatus is the outcome of a brainstorm turn (init-plan §9).
type BrainstormStatus string

const (
	NeedsAnswers     BrainstormStatus = "needs_answers"
	SpecReady        BrainstormStatus = "spec_ready"
	BrainstormFailed BrainstormStatus = "failed"
)

// PhaseStatus is the outcome of a plan or execute turn (init-plan §9).
type PhaseStatus string

const (
	StatusPlanReady PhaseStatus = "plan_ready" // plan
	StatusComplete  PhaseStatus = "complete"   // execute
	StatusFailed    PhaseStatus = "failed"
)

// BrainstormInput / BrainstormResult — the §9 brainstorm contract.
type BrainstormInput struct {
	Transcript string
}
type BrainstormResult struct {
	Status       BrainstormStatus
	Questions    []string
	SpecMarkdown string
	Error        string // set when Status == BrainstormFailed
}

// PlanInput / PlanResult — the §9 plan contract.
type PlanInput struct {
	Transcript string
	Spec       string
}
type PlanResult struct {
	Status   PhaseStatus
	PlanPath string
	Summary  string
	Error    string
}

// ExecuteInput / ExecuteResult — the §9 execute contract.
type ExecuteInput struct {
	Transcript string
	PlanPath   string
}
type ExecuteResult struct {
	Status      PhaseStatus
	Branch      string
	Commits     []string
	TestSummary string
	Notes       string
	Error       string
}

// Brain is the reasoning surface. Faked in M1 (CannedBrain); the real
// claude-CLI implementation (internal/claude) lands in M2.
type Brain interface {
	Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error)
	Plan(ctx context.Context, in PlanInput) (PlanResult, error)
	Execute(ctx context.Context, in ExecuteInput) (ExecuteResult, error)
}
