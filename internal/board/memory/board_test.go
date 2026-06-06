package memory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestMemoryBoardMoveAndComment(t *testing.T) {
	ctx := context.Background()
	b := New()
	b.Seed(board.Card{ID: "I1", Repo: "o/r", Title: "idea", Body: "body", Phase: board.PhaseInbox})

	if err := b.MoveTo(ctx, "I1", board.PhaseBrainstorming); err != nil {
		t.Fatalf("MoveTo: %v", err)
	}
	if err := b.PostComment(ctx, "I1", "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if err := b.SetBody(ctx, "I1", "new spec"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}

	got, err := b.GetCard(ctx, "I1")
	if err != nil {
		t.Fatalf("GetCard: %v", err)
	}
	if got.Phase != board.PhaseBrainstorming {
		t.Errorf("phase = %q, want Brainstorming", got.Phase)
	}
	if got.Body != "new spec" {
		t.Errorf("body = %q, want 'new spec'", got.Body)
	}
	if len(got.Comments) != 1 || !got.Comments[0].IsBot || got.Comments[0].Body != "hello" {
		t.Errorf("comments = %+v, want one bot comment 'hello'", got.Comments)
	}

	// GetCard returns a copy: mutating it must not change stored state.
	got.Comments[0].Body = "tampered"
	again, _ := b.GetCard(ctx, "I1")
	if again.Comments[0].Body != "hello" {
		t.Error("GetCard must return a defensive copy of comments")
	}
}

func TestMemoryBoardListCards(t *testing.T) {
	ctx := context.Background()
	b := New()
	b.Seed(board.Card{ID: "A", Phase: board.PhaseInbox})
	b.Seed(board.Card{ID: "B", Phase: board.PhaseInbox})
	b.Seed(board.Card{ID: "C", Phase: board.PhaseSpecReview})

	got, err := b.ListCards(ctx, board.PhaseInbox)
	if err != nil {
		t.Fatalf("ListCards: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListCards(Inbox) = %d cards, want 2", len(got))
	}
}

func TestMemoryBoardParseEvent(t *testing.T) {
	b := New()
	in := board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhasePlanning, Dedup: "d1"}
	payload, _ := json.Marshal(in)
	got, err := b.ParseEvent(nil, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if got.CardID != "I1" || got.NewPhase != board.PhasePlanning || got.Dedup != "d1" {
		t.Errorf("ParseEvent = %+v, want the round-tripped event", got)
	}
}

func TestMemoryBoardMissingCardErrors(t *testing.T) {
	if _, err := New().GetCard(context.Background(), "nope"); err == nil {
		t.Error("GetCard on a missing card should error")
	}
}
