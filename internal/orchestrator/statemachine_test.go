package orchestrator

import (
	"context"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// TestFullStateMachine drives Inbox -> ... -> PRReview and checks idempotency.
func TestFullStateMachine(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "Add login", Body: "We need auth", Phase: board.PhaseInbox})

	brain := &scriptedBrain{
		brainstorm: []BrainstormResult{
			{Status: NeedsAnswers, Questions: []string{"Which provider?"}},
			{Status: SpecReady, SpecMarkdown: "## Auth spec"},
		},
		plan:    []PlanResult{{Status: StatusPlanReady}},
		execute: []ExecuteResult{{Status: StatusComplete, Branch: "feat/auth"}},
	}
	ff := &fakeForge{prURL: "https://example/pr/7", wtPath: "/wt/o-r-7"}
	st := store.NewMemory()
	w := NewWorker(b, ff, brain, st, nil)

	mustPhase := func(want board.Phase) {
		t.Helper()
		c, _ := b.GetCard(ctx, "I1")
		if c.Phase != want {
			t.Fatalf("phase = %q, want %q", c.Phase, want)
		}
	}

	// 1. Card created -> pick up -> brainstorm (needs_answers) -> AwaitingAnswers.
	if err := w.Process(ctx, board.Event{Kind: board.EventCardCreated, CardID: "I1", Dedup: "d1"}); err != nil {
		t.Fatal(err)
	}
	mustPhase(board.PhaseAwaitingAnswers)

	// 2. Human answers -> brainstorm (spec_ready) -> SpecReview, body rewritten.
	b.AddComment("I1", board.Comment{ID: "h1", IsBot: false, Body: "OAuth via GitHub"})
	if err := w.Process(ctx, board.Event{Kind: board.EventCommentAdded, CardID: "I1", Dedup: "d2", Comment: &board.Comment{ID: "h1", IsBot: false}}); err != nil {
		t.Fatal(err)
	}
	mustPhase(board.PhaseSpecReview)
	if c, _ := b.GetCard(ctx, "I1"); c.Body != "## Auth spec" {
		t.Fatalf("spec body = %q, want '## Auth spec'", c.Body)
	}

	// 3. Human approves by moving to Planning -> plan -> build -> open PR -> PRReview.
	if err := b.MoveTo(ctx, "I1", board.PhasePlanning); err != nil {
		t.Fatal(err)
	}
	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning, Dedup: "d3"}); err != nil {
		t.Fatal(err)
	}
	mustPhase(board.PhasePRReview)
	if !ff.pushed {
		t.Error("branch should have been pushed")
	}

	// 4. Idempotency: replaying the approval and the answer changes nothing.
	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning, Dedup: "d3"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Process(ctx, board.Event{Kind: board.EventCommentAdded, CardID: "I1", Dedup: "d2", Comment: &board.Comment{ID: "h1", IsBot: false}}); err != nil {
		t.Fatal(err)
	}
	mustPhase(board.PhasePRReview)
}
