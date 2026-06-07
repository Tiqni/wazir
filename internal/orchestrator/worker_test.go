package orchestrator

import (
	"context"
	"errors"
	"strings"
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

	brainstormCalls  int    // how many times Brainstorm was invoked
	lastExecPlanPath string // records the PlanPath the last Execute call received
}

func (s *scriptedBrain) Brainstorm(ctx context.Context, in BrainstormInput) (BrainstormResult, error) {
	s.brainstormCalls++
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
	s.lastExecPlanPath = in.PlanPath
	if s.err != nil {
		return ExecuteResult{}, s.err
	}
	r := s.execute[0]
	s.execute = s.execute[1:]
	return r, nil
}

// fakeForge satisfies forge.CodeForge and records the op sequence. PushBranch/
// OpenPR succeed by default; CreateWorktree returns wtPath.
type fakeForge struct {
	prURL   string
	pushErr error
	wtPath  string   // path CreateWorktree returns ("" by default)
	pushed  bool
	removed bool
	calls   []string // ordered: ensureClone, createWorktree, push, openPR, removeWorktree
}

func (f *fakeForge) EnsureClone(ctx context.Context, repo string) error {
	f.calls = append(f.calls, "ensureClone")
	return nil
}
func (f *fakeForge) CreateWorktree(ctx context.Context, repo, branch string) (string, error) {
	f.calls = append(f.calls, "createWorktree")
	return f.wtPath, nil
}
func (f *fakeForge) RemoveWorktree(ctx context.Context, repo, path string) error {
	f.calls = append(f.calls, "removeWorktree")
	f.removed = true
	return nil
}
func (f *fakeForge) PushBranch(ctx context.Context, repo, branch string) error {
	f.calls = append(f.calls, "push")
	if f.pushErr != nil {
		return f.pushErr
	}
	f.pushed = true
	return nil
}
func (f *fakeForge) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, error) {
	f.calls = append(f.calls, "openPR")
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
		plan:    []PlanResult{{Status: StatusPlanReady, PlanPath: "docs/plan.md"}},
		execute: []ExecuteResult{{Status: StatusComplete, Branch: "feat/x"}},
	}
	ff := &fakeForge{prURL: "https://x/pr/1"}
	st := store.NewMemory()
	w := NewWorker(b, ff, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if brain.lastExecPlanPath != "docs/plan.md" {
		t.Errorf("execute got PlanPath %q, want docs/plan.md (threaded from plan)", brain.lastExecPlanPath)
	}
	if rec, _, _ := st.GetCard("I1"); rec.PlanPath != "docs/plan.md" {
		t.Errorf("plan path persisted = %q, want docs/plan.md", rec.PlanPath)
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

// A direct Building re-entry (ActExecute) must recover the plan path persisted
// by an earlier Planning turn, rather than executing against an empty path.
func TestWorkerExecuteLoadsPersistedPlanPath(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBuilding})
	st := store.NewMemory()
	if err := st.PutCard("I1", store.CardRecord{Repo: "o/r", PlanPath: "docs/plan.md"}); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	brain := &scriptedBrain{execute: []ExecuteResult{{Status: StatusComplete, Branch: "feat/x"}}}
	w := NewWorker(b, &fakeForge{prURL: "https://x/pr/1"}, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if brain.lastExecPlanPath != "docs/plan.md" {
		t.Errorf("execute got PlanPath %q, want the persisted docs/plan.md", brain.lastExecPlanPath)
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
	ff := &fakeForge{pushErr: forge.ErrNotImplemented, wtPath: "/wt/keep"}
	w := NewWorker(b, ff, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (M4 forge stub)", card.Phase)
	}
	if ff.removed {
		t.Error("worktree must be KEPT when push fails, but RemoveWorktree was called")
	}
}

func TestBranchSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Add subtract", "add-subtract"},
		{"", "card"},
		{"!!!", "card"},
		{"  Fix   the BUG!!  ", "fix-the-bug"},
	}
	for _, tc := range cases {
		got := branchSlug(tc.in)
		if got != tc.want {
			t.Errorf("branchSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// 50-char all-letter title: result must be <= 40 chars and have no leading/trailing dash.
	title50 := strings.Repeat("a", 50)
	slug50 := branchSlug(title50)
	if len(slug50) > 40 {
		t.Errorf("branchSlug(50-letter title) length = %d, want <= 40", len(slug50))
	}
	if strings.HasPrefix(slug50, "-") || strings.HasSuffix(slug50, "-") {
		t.Errorf("branchSlug(50-letter title) = %q has leading/trailing dash", slug50)
	}

	// branchName combines issue number and slug.
	if got := branchName(7, "Add subtract"); got != "feature/issue-7-add-subtract" {
		t.Errorf("branchName(7, \"Add subtract\") = %q, want \"feature/issue-7-add-subtract\"", got)
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

func TestWorkerBrainstormFailedGoesToFailed(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: BrainstormFailed, Error: "bad contract"}}}
	w := NewWorker(b, &fakeForge{}, brain, store.NewMemory(), nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
}

func TestWorkerBrainstormCountsTurns(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q?"}}}}
	st := store.NewMemory()
	w := NewWorker(b, &fakeForge{}, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if rec, _, _ := st.GetCard("I1"); rec.BrainstormTurns != 1 {
		t.Errorf("BrainstormTurns = %d, want 1", rec.BrainstormTurns)
	}
}

func TestWorkerBrainstormCapEscalates(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", BrainstormTurns: 2})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: NeedsAnswers, Questions: []string{"q?"}}}}
	w := NewWorker(b, &fakeForge{}, brain, st, nil).WithMaxBrainstormTurns(2)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseAwaitingAnswers {
		t.Errorf("phase = %q, want AwaitingAnswers (escalation stays put)", card.Phase)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "limit") {
		t.Errorf("want a single 'limit' escalation comment, got %+v", card.Comments)
	}
	if rec, _, _ := st.GetCard("I1"); rec.BrainstormTurns != 2 {
		t.Errorf("BrainstormTurns = %d, want unchanged 2 (no increment past the cap)", rec.BrainstormTurns)
	}
	// The cap must short-circuit BEFORE the (paid) model call — no further spend.
	if brain.brainstormCalls != 0 {
		t.Errorf("brain called %d times past the cap, want 0 (no further spend)", brain.brainstormCalls)
	}
}

func TestWorkerSpecReadyResetsTurns(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Phase: board.PhaseBrainstorming})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", BrainstormTurns: 3})
	brain := &scriptedBrain{brainstorm: []BrainstormResult{{Status: SpecReady, SpecMarkdown: "SPEC"}}}
	w := NewWorker(b, &fakeForge{}, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if rec, _, _ := st.GetCard("I1"); rec.BrainstormTurns != 0 {
		t.Errorf("BrainstormTurns = %d, want reset to 0", rec.BrainstormTurns)
	}
}

func TestWorkerApprovalCreatesWorktreeAndCleansUp(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "Add subtract", Phase: board.PhasePlanning})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", IssueNumber: 7})
	brain := &scriptedBrain{
		plan:    []PlanResult{{Status: StatusPlanReady, PlanPath: "docs/plan.md"}},
		execute: []ExecuteResult{{Status: StatusComplete, Branch: "claude-reported-ignored"}},
	}
	ff := &fakeForge{prURL: "https://x/pr/1", wtPath: "/wt/o-r-7"}
	w := NewWorker(b, ff, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	wantSeq := []string{"ensureClone", "createWorktree", "push", "openPR", "removeWorktree"}
	if strings.Join(ff.calls, ",") != strings.Join(wantSeq, ",") {
		t.Errorf("forge call sequence = %v, want %v", ff.calls, wantSeq)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.Branch != "feature/issue-7-add-subtract" {
		t.Errorf("persisted branch = %q, want feature/issue-7-add-subtract", rec.Branch)
	}
	if rec.WorktreePath != "/wt/o-r-7" {
		t.Errorf("persisted worktree = %q, want /wt/o-r-7", rec.WorktreePath)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview", card.Phase)
	}
}

func TestWorkerFailureKeepsWorktree(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "t", Phase: board.PhasePlanning})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "o/r", IssueNumber: 7})
	brain := &scriptedBrain{plan: []PlanResult{{Status: StatusFailed, Error: "boom"}}}
	ff := &fakeForge{wtPath: "/wt/o-r-7"}
	w := NewWorker(b, ff, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(ctx, "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	if ff.removed {
		t.Error("worktree must be KEPT on failure for debugging, but RemoveWorktree was called")
	}
}
