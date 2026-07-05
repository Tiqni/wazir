package orchestrator

import (
	"context"

	"github.com/EmadMokhtar/wazir/internal/forge"
)

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
	RepoPath   string // M5: the card's repo clone; used as the claude cmd.Dir so the target repo's CLAUDE.md/AGENTS.md load
}
type BrainstormResult struct {
	Status       BrainstormStatus
	Questions    []string
	SpecMarkdown string
	Error        string // set when Status == BrainstormFailed
}

// PlanInput / PlanResult — the §9 plan contract.
type PlanInput struct {
	Transcript   string
	Spec         string
	WorktreePath string // M4: cmd.Dir for the headless claude run
}
type PlanResult struct {
	Status   PhaseStatus
	PlanPath string
	Summary  string
	Error    string
}

// ExecuteInput / ExecuteResult — the §9 execute contract.
type ExecuteInput struct {
	Transcript   string
	PlanPath     string
	WorktreePath string // M4: cmd.Dir for the headless claude run
}
type ExecuteResult struct {
	Status      PhaseStatus
	Branch      string
	Commits     []string
	TestSummary string
	Notes       string
	Error       string
}

// ReworkInput / ReworkResult — the phase-2 rework contract.
type ReworkInput struct {
	Transcript    string
	WorktreePath  string // cmd.Dir for the headless claude run (the PR-head worktree)
	Feedback      forge.ReviewFeedback
	FailingChecks []string
	Annotations   []forge.CheckAnnotation
	Instruction   string // human's directed fix ("use a mutex here"); empty for a bare rework
}
type ReworkResult struct {
	Status PhaseStatus // StatusComplete | StatusFailed
	Notes  string
	Error  string
}

// Brain is the reasoning surface. Faked in M1 (CannedBrain); the real
// claude-CLI implementation (internal/claude) lands in M2.
type Brain interface {
	Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error)
	Plan(ctx context.Context, in PlanInput) (PlanResult, error)
	Execute(ctx context.Context, in ExecuteInput) (ExecuteResult, error)
	Rework(ctx context.Context, in ReworkInput) (ReworkResult, error)
}
