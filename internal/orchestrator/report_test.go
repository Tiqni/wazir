package orchestrator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/forge"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func reportSetup(t *testing.T, status forge.PRStatus, statusErr error, last store.CardRecord) (*memboard.Board, *store.Memory, *fakeForge, *Worker) {
	t.Helper()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhasePRReview})
	st := store.NewMemory()
	last.Repo = "octocat/hello"
	if last.PRNumber == 0 {
		last.PRNumber = 9
	}
	st.PutCard("I1", last)
	ff := &fakeForge{prStatus: status, prStatusErr: statusErr}
	w := NewWorker(b, ff, &scriptedBrain{}, st, nil)
	return b, st, ff, w
}

func process(t *testing.T, w *Worker, kind board.EventKind) {
	t.Helper()
	if err := w.Process(context.Background(), board.Event{Kind: kind, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
}

func TestReportHealthyStateResetsReworkBudget(t *testing.T) {
	// A card that has burned rework rounds but now observes green CI: the rework
	// budget resets, so a future legitimate review round isn't starved by the cap.
	b, st, _, w := reportSetup(t, forge.PRStatus{CIConclusion: "success"}, nil, store.CardRecord{ReworkRounds: 3})
	process(t, w, board.EventChecksCompleted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview (green CI is informational)", card.Phase)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 0 {
		t.Errorf("ReworkRounds = %d, want 0 (green CI resets the rework budget)", rec.ReworkRounds)
	}
}

func TestReportAlreadyGreenNoDeltaStillResetsBudget(t *testing.T) {
	// The PR was already green (LastCIConclusion=success) and stays green — no report
	// delta — but a prior rework burned rounds. The budget must still reset, so a
	// healthy PR a human iterates on isn't starved by the cap.
	b, st, _, w := reportSetup(t, forge.PRStatus{CIConclusion: "success"}, nil,
		store.CardRecord{LastCIConclusion: "success", ReworkRounds: 3})
	process(t, w, board.EventChecksCompleted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview", card.Phase)
	}
	if len(card.Comments) != 0 {
		t.Errorf("no report delta -> no comment; got %+v", card.Comments)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 0 {
		t.Errorf("ReworkRounds = %d, want 0 (already-green still resets)", rec.ReworkRounds)
	}
}

func TestReportUnhealthyStateKeepsReworkBudget(t *testing.T) {
	// Red CI must NOT reset the budget — an unproductive rut keeps counting to the cap.
	b, st, _, w := reportSetup(t, forge.PRStatus{CIConclusion: "failure", FailingChecks: []string{"lint"}}, nil, store.CardRecord{ReworkRounds: 2})
	process(t, w, board.EventChecksCompleted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.ReworkRounds != 2 {
		t.Errorf("ReworkRounds = %d, want 2 (red CI must not reset the budget)", rec.ReworkRounds)
	}
}

func TestReportChangesRequestedMovesToFailed(t *testing.T) {
	b, st, _, w := reportSetup(t, forge.PRStatus{ReviewDecision: "changes_requested"}, nil, store.CardRecord{})
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "Changes requested") {
		t.Errorf("comment = %+v", card.Comments)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "changes_requested" {
		t.Errorf("LastReviewState = %q", rec.LastReviewState)
	}
}

func TestReportCIFailureMovesToFailedWithNames(t *testing.T) {
	b, _, _, w := reportSetup(t, forge.PRStatus{CIConclusion: "failure", FailingChecks: []string{"lint", "unit"}}, nil, store.CardRecord{})
	process(t, w, board.EventChecksCompleted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "lint") {
		t.Errorf("comment should name failing checks: %+v", card.Comments)
	}
}

func TestReportApprovedDoesNotMove(t *testing.T) {
	b, st, _, w := reportSetup(t, forge.PRStatus{ReviewDecision: "approved"}, nil, store.CardRecord{})
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview (approved is informational)", card.Phase)
	}
	if len(card.Comments) != 1 {
		t.Errorf("want 1 informational comment, got %d", len(card.Comments))
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "approved" {
		t.Errorf("LastReviewState = %q, want approved (delta persisted even without a move)", rec.LastReviewState)
	}
}

func TestReportDeltaSuppressesRepeat(t *testing.T) {
	// Persisted last state already == changes_requested -> nothing changed.
	b, st, ff, w := reportSetup(t, forge.PRStatus{ReviewDecision: "changes_requested"}, nil, store.CardRecord{LastReviewState: "changes_requested"})
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	if len(card.Comments) != 0 {
		t.Errorf("delta should suppress the comment; got %+v", card.Comments)
	}
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview (no change => no move)", card.Phase)
	}
	if !slices.Contains(ff.calls, "prStatus") {
		t.Errorf("PRStatus should still be fetched; calls=%v", ff.calls)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "changes_requested" {
		t.Errorf("delta state should be unchanged, got %q", rec.LastReviewState)
	}
}

func TestReportReadErrorIsSoft(t *testing.T) {
	b, st, _, w := reportSetup(t, forge.PRStatus{}, errors.New("boom"), store.CardRecord{})
	process(t, w, board.EventChecksCompleted) // must return nil (no fail())

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("a read error must not move the card; phase = %q", card.Phase)
	}
	if len(card.Comments) != 0 {
		t.Errorf("a read error must not comment; got %+v", card.Comments)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "" || rec.LastCIConclusion != "" {
		t.Errorf("a read error must not write delta state; rec=%+v", rec)
	}
}

func TestReportSkipsWhenNoPRNumber(t *testing.T) {
	// A card with no persisted PR number can't be looked up — skip silently
	// (no fetch, no comment, no move), don't fail it.
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhasePRReview})
	st := store.NewMemory()
	st.PutCard("I1", store.CardRecord{Repo: "octocat/hello"}) // PRNumber == 0
	ff := &fakeForge{prStatus: forge.PRStatus{CIConclusion: "failure"}}
	w := NewWorker(b, ff, &scriptedBrain{}, st, nil)

	process(t, w, board.EventChecksCompleted)

	if slices.Contains(ff.calls, "prStatus") {
		t.Errorf("must not fetch PRStatus without a PR number; calls=%v", ff.calls)
	}
	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview || len(card.Comments) != 0 {
		t.Errorf("no-PR-number must be a silent skip; phase=%q comments=%+v", card.Phase, card.Comments)
	}
}
