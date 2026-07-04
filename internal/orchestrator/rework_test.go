package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func reworkSetup(t *testing.T, rounds int) (*memboard.Board, *store.Memory, *fakeForge, *scriptedBrain, *Worker) {
	t.Helper()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhaseReworking})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "octocat/hello", PRNumber: 9, Branch: "feature/issue-7-t", ReworkRounds: rounds})
	ff := &fakeForge{worktreeFromBranch: "/wt", prURL: "https://x/pull/9", prNumber: 9}
	brain := &scriptedBrain{}
	w := NewWorker(b, ff, brain, st, nil).WithMaxReworkRounds(3)
	return b, st, ff, brain, w
}

func TestReworkSuccessRepushesAndReturnsToPRReview(t *testing.T) {
	b, st, ff, brain, w := reworkSetup(t, 0)
	brain.rework = []ReworkResult{{Status: StatusComplete, Notes: "fixed"}}

	if err := w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview", card.Phase)
	}
	if !slices.Contains(ff.calls, "createWorktreeFromBranch") || !slices.Contains(ff.calls, "push") {
		t.Errorf("expected worktree-from-branch + push; calls=%v", ff.calls)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 1 {
		t.Errorf("ReworkRounds = %d, want 1", rec.ReworkRounds)
	}
}

func TestReworkFailureMovesToFailedAndIncrements(t *testing.T) {
	b, st, _, brain, w := reworkSetup(t, 0)
	brain.rework = []ReworkResult{{Status: StatusFailed, Error: "still broken"}}

	if err := w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 1 {
		t.Errorf("ReworkRounds = %d, want 1", rec.ReworkRounds)
	}
}

func TestReworkCapEscalatesWithoutSpending(t *testing.T) {
	b, _, ff, brain, w := reworkSetup(t, 3) // already at the cap
	brain.rework = []ReworkResult{{Status: StatusComplete}}

	if err := w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (cap escalation)", card.Phase)
	}
	if slices.Contains(ff.calls, "createWorktreeFromBranch") {
		t.Errorf("cap escalation must not create a worktree; calls=%v", ff.calls)
	}
	if len(card.Comments) != 1 {
		t.Errorf("want an escalation comment, got %d", len(card.Comments))
	}
}

func TestReworkTriggerBMovesToReworkingFirst(t *testing.T) {
	// Card in PRReview, triggered by the @wazir fix command -> worker moves it to
	// Reworking, then reworks.
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhasePRReview})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "octocat/hello", PRNumber: 9, Branch: "feature/issue-7-t"})
	ff := &fakeForge{worktreeFromBranch: "/wt"}
	brain := &scriptedBrain{rework: []ReworkResult{{Status: StatusComplete}}}
	w := NewWorker(b, ff, brain, st, nil).WithMaxReworkRounds(3)

	if err := w.Process(context.Background(), board.Event{Kind: board.EventReworkRequested, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview { // ends back in PRReview after a successful rework
		t.Errorf("phase = %q, want PRReview", card.Phase)
	}
	if !slices.Contains(ff.calls, "createWorktreeFromBranch") {
		t.Errorf("trigger B should have reworked; calls=%v", ff.calls)
	}
}

func TestReworkInfraErrorDoesNotIncrement(t *testing.T) {
	b, st, ff, brain, w := reworkSetup(t, 0)
	ff.ensureCloneErr = errors.New("boom") // clone fails before any turn
	brain.rework = []ReworkResult{{Status: StatusComplete}}

	_ = w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking})
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (infra error -> fail path)", card.Phase)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 0 {
		t.Errorf("ReworkRounds = %d, want 0 (no turn ran)", rec.ReworkRounds)
	}
}

func TestReworkPushFailureIncrementsRound(t *testing.T) {
	// A turn ran (StatusComplete) but the push failed — the round still counts
	// (spec: "a turn ran → count it") and the card drops to Failed.
	b, st, ff, brain, w := reworkSetup(t, 0)
	brain.rework = []ReworkResult{{Status: StatusComplete, Notes: "fixed"}}
	ff.pushErr = errors.New("network down")

	_ = w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking})
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (push failed after a real turn)", card.Phase)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 1 {
		t.Errorf("ReworkRounds = %d, want 1 (the turn ran)", rec.ReworkRounds)
	}
}

func TestReworkMissingPRFailsClosed(t *testing.T) {
	// A card dragged to Reworking without a persisted PR/branch can't be reworked;
	// fail closed rather than run a turn against nothing.
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhaseReworking})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "octocat/hello"}) // no PRNumber/Branch
	ff := &fakeForge{worktreeFromBranch: "/wt"}
	brain := &scriptedBrain{rework: []ReworkResult{{Status: StatusComplete}}}
	w := NewWorker(b, ff, brain, st, nil).WithMaxReworkRounds(3)

	if err := w.Process(context.Background(), board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseReworking}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed (no PR/branch)", card.Phase)
	}
	if slices.Contains(ff.calls, "createWorktreeFromBranch") {
		t.Errorf("must not attempt a worktree without a PR/branch; calls=%v", ff.calls)
	}
}
