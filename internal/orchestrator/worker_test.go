package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/forge"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// scriptedBrain pops a pre-loaded result per call.
type scriptedBrain struct {
	brainstorm []BrainstormResult
	plan       []PlanResult
	execute    []ExecuteResult
	err        error
}

func (s *scriptedBrain) Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error) {
	if s.err != nil {
		return BrainstormResult{}, s.err
	}
	r := s.brainstorm[0]
	s.brainstorm = s.brainstorm[1:]
	return r, nil
}
func (s *scriptedBrain) Plan(ctx context.Context, in PlanInput) (PlanResult, error) {
	if s.err != nil {
		return PlanResult{}, s.err
	}
	r := s.plan[0]
	s.plan = s.plan[1:]
	return r, nil
}
func (s *scriptedBrain) Execute(ctx context.Context, in ExecuteInput) (ExecuteResult, error) {
	if s.err != nil {
		return ExecuteResult{}, s.err
	}
	r := s.execute[0]
	s.execute = s.execute[1:]
	return r, nil
}

// fakeForge satisfies forge.CodeForge; PushBranch/OpenPR succeed, the rest no-op.
type fakeForge struct {
	pushed  bool
	prURL   string
	pushErr error
}

func (f *fakeForge) Clone(ctx context.Context, repo, dest string) error               { return nil }
func (f *fakeForge) CreateWorktree(ctx context.Context, repo, branch, p string) error { return nil }
func (f *fakeForge) RemoveWorktree(ctx context.Context, p string) error               { return nil }
func (f *fakeForge) PushBranch(ctx context.Context, repo, branch string) error {
	if f.pushErr != nil {
		return f.pushErr
	}
	f.pushed = true
	return nil
}
func (f *fakeForge) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, error) {
	return f.prURL, nil
}

var _ forge.CodeForge = (*fakeForge)(nil)

func TestWorkerBrainstormNeedsAnswers(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "t", Phase: board.PhaseInbox})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q1?"}}}}
	w := NewWorker(b, &fakeForge{}, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventCardCreated, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseAwaitingAnswers {
		t.Errorf("phase = %q, want AwaitingAnswers", card.Phase)
	}
	if len(card.Comments) != 1 {
		t.Errorf("want 1 posted question comment, got %d", len(card.Comments))
	}
}

func TestWorkerBrainstormSpecReady(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: SpecReady, SpecMarkdown: "SPEC"}}}
	w := NewWorker(b, &fakeForge{}, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseBrainstorming}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseSpecReview || card.Body != "SPEC" {
		t.Errorf("phase=%q body=%q, want SpecReview/SPEC", card.Phase, card.Body)
	}
}

func TestWorkerApprovalRunsPlanBuildPR(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "t", Phase: board.PhasePlanning})
	brain := &scriptedBrain{
		plan:    []PlanResult{{Status: StatusPlanReady}},
		execute: []ExecuteResult{{Status: StatusComplete, Branch: "feat/x"}},
	}
	ff := &fakeForge{prURL: "https://x/pr/1"}
	w := NewWorker(b, ff, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview", card.Phase)
	}
	if !ff.pushed {
		t.Error("expected the branch to be pushed")
	}
	if len(card.Comments) != 1 {
		t.Errorf("want 1 PR-link comment, got %d", len(card.Comments))
	}
}

func TestWorkerFailPathOnBrainError(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	brain := &scriptedBrain{err: errors.New("boom")}
	w := NewWorker(b, &fakeForge{}, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process should swallow handled failures: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
}

func TestWorkerFailPathOnForgeNotImplemented(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBuilding})
	brain := &scriptedBrain{execute: []ExecuteResult{{Status: StatusComplete, Branch: "feat/x"}}}
	ff := &fakeForge{pushErr: forge.ErrNotImplemented}
	w := NewWorker(b, ff, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (M4 forge stub)", card.Phase)
	}
}

func TestWorkerActNoneDoesNothing(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhasePRReview})
	w := NewWorker(b, &fakeForge{}, &scriptedBrain{}, store.NewMemory(), nil)

	// A comment on a PRReview card resolves to ActNone — nothing should change.
	ev := board.Event{Kind: board.EventCommentAdded, CardID: "I1", Comment: &board.Comment{ID: "h1", IsBot: false}}
	if err := w.Process(ctx, ev); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhasePRReview || len(card.Comments) != 0 {
		t.Errorf("ActNone must not change the card: phase=%q comments=%d", card.Phase, len(card.Comments))
	}
}

func TestWorkerIdempotentOnReprocessedComment(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseAwaitingAnswers})
	b.AddComment("I1", board.Comment{ID: "h1", IsBot: false, Body: "answer"})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: SpecReady, SpecMarkdown: "SPEC"}}}
	st := store.NewMemory()
	w := NewWorker(b, &fakeForge{}, brain, st, nil)

	ev := board.Event{Kind: board.EventCommentAdded, CardID: "I1", Comment: &board.Comment{ID: "h1", IsBot: false}}
	if err := w.Process(ctx, ev); err != nil {
		t.Fatalf("first Process: %v", err)
	}
	// Re-deliver the same comment: must be a no-op (no second brainstorm pop).
	if err := w.Process(ctx, ev); err != nil {
		t.Fatalf("second Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseSpecReview {
		t.Errorf("phase = %q, want SpecReview (unchanged by the replay)", card.Phase)
	}
}
