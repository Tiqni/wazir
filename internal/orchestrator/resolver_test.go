package orchestrator

import (
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestResolverTable(t *testing.T) {
	humanComment := &board.Comment{ID: "h1", IsBot: false, Body: "hi"}
	botComment := &board.Comment{ID: "b1", IsBot: true, Body: "marker"}

	cases := []struct {
		name      string
		phase     board.Phase
		ev        board.Event
		lastComID string
		want      Action
	}{
		{"inbox created -> pickup", board.PhaseInbox, board.Event{Kind: board.EventCardCreated}, "", ActPickUp},
		{"inbox moved to brainstorming -> pickup", board.PhaseInbox, board.Event{Kind: board.EventPhaseChanged, NewPhase: board.PhaseBrainstorming}, "", ActPickUp},
		{"inbox stray comment -> none", board.PhaseInbox, board.Event{Kind: board.EventCommentAdded, Comment: humanComment}, "", ActNone},
		{"brainstorming -> brainstorm", board.PhaseBrainstorming, board.Event{Kind: board.EventPhaseChanged, NewPhase: board.PhaseBrainstorming}, "", ActBrainstorm},
		{"awaiting + human comment -> brainstorm", board.PhaseAwaitingAnswers, board.Event{Kind: board.EventCommentAdded, Comment: humanComment}, "", ActBrainstorm},
		{"awaiting + bot comment -> none", board.PhaseAwaitingAnswers, board.Event{Kind: board.EventCommentAdded, Comment: botComment}, "", ActNone},
		{"awaiting + phase change -> none", board.PhaseAwaitingAnswers, board.Event{Kind: board.EventPhaseChanged}, "", ActNone},
		{"specreview moved to planning -> plan", board.PhaseSpecReview, board.Event{Kind: board.EventPhaseChanged, NewPhase: board.PhasePlanning}, "", ActPlan},
		{"specreview approval signal -> plan", board.PhaseSpecReview, board.Event{Kind: board.EventApprovalGiven, Signal: board.SignalApproveSpec}, "", ActPlan},
		{"specreview revision comment -> brainstorm", board.PhaseSpecReview, board.Event{Kind: board.EventCommentAdded, Comment: humanComment}, "", ActBrainstorm},
		{"planning -> plan", board.PhasePlanning, board.Event{Kind: board.EventPhaseChanged, NewPhase: board.PhasePlanning}, "", ActPlan},
		{"building -> execute", board.PhaseBuilding, board.Event{Kind: board.EventPhaseChanged}, "", ActExecute},
		{"prreview -> none", board.PhasePRReview, board.Event{Kind: board.EventPhaseChanged}, "", ActNone},
		{"done -> none", board.PhaseDone, board.Event{Kind: board.EventCommentAdded, Comment: humanComment}, "", ActNone},
		{"failed -> none", board.PhaseFailed, board.Event{Kind: board.EventPhaseChanged}, "", ActNone},
		{"already-processed comment -> none", board.PhaseAwaitingAnswers, board.Event{Kind: board.EventCommentAdded, Comment: humanComment}, "h1", ActNone},
		{"awaiting + nil-comment event -> none", board.PhaseAwaitingAnswers, board.Event{Kind: board.EventCommentAdded}, "", ActNone},
	}

	var r Resolver
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := board.Card{ID: "I1", Phase: tc.phase}
			got := r.Resolve(card, tc.ev, tc.lastComID)
			if got.Action != tc.want {
				t.Errorf("Resolve = %s, want %s", got.Action, tc.want)
			}
		})
	}
}
