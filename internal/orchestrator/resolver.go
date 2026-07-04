package orchestrator

import "github.com/EmadMokhtar/wazir/internal/board"

// Resolver maps (phase, event, what's-new) to a Decision. Pure: no I/O.
type Resolver struct{}

// Resolve decides the action for one event against the card's current phase.
func (Resolver) Resolve(card board.Card, ev board.Event, lastCommentID string) Decision {
	// A comment event with no comment payload is malformed — ignore it.
	if ev.Kind == board.EventCommentAdded && ev.Comment == nil {
		return Decision{ActNone}
	}
	// Bot-authored comments never drive work (loop prevention, §8.1).
	if ev.Kind == board.EventCommentAdded && ev.Comment != nil && ev.Comment.IsBot {
		return Decision{ActNone}
	}
	// A comment already processed is a no-op (idempotency, §8.7).
	if ev.Kind == board.EventCommentAdded && ev.Comment != nil && ev.Comment.ID == lastCommentID {
		return Decision{ActNone}
	}

	switch card.Phase {
	case board.PhaseInbox:
		if ev.Kind == board.EventCardCreated ||
			(ev.Kind == board.EventPhaseChanged && ev.NewPhase == board.PhaseBrainstorming) {
			return Decision{ActPickUp}
		}
		return Decision{ActNone}

	case board.PhaseBrainstorming:
		return Decision{ActBrainstorm}

	case board.PhaseAwaitingAnswers:
		if ev.Kind == board.EventCommentAdded {
			return Decision{ActBrainstorm}
		}
		return Decision{ActNone}

	case board.PhaseSpecReview:
		// Approval: a human moved the card to Planning, or gave an approval signal.
		if ev.Kind == board.EventPhaseChanged && ev.NewPhase == board.PhasePlanning {
			return Decision{ActPlan}
		}
		if ev.Kind == board.EventApprovalGiven && ev.Signal == board.SignalApproveSpec {
			return Decision{ActPlan}
		}
		// Otherwise a human comment requests a spec revision.
		if ev.Kind == board.EventCommentAdded {
			return Decision{ActBrainstorm}
		}
		return Decision{ActNone}

	case board.PhasePlanning:
		return Decision{ActPlan}

	case board.PhaseBuilding:
		return Decision{ActExecute}

	case board.PhasePRReview:
		if ev.Kind == board.EventReviewSubmitted || ev.Kind == board.EventChecksCompleted {
			return Decision{ActReport}
		}
		if ev.Kind == board.EventReworkRequested {
			return Decision{ActRework}
		}
		return Decision{ActNone}

	case board.PhaseReworking:
		// A stale review/CI webhook from the PR's prior push must not queue a second
		// rework while one is already in flight here; only a rework trigger acts.
		if ev.Kind == board.EventReviewSubmitted || ev.Kind == board.EventChecksCompleted {
			return Decision{ActNone}
		}
		return Decision{ActRework}

	default: // Done, Failed
		if card.Phase == board.PhaseFailed && ev.Kind == board.EventReworkRequested {
			return Decision{ActRework}
		}
		return Decision{ActNone}
	}
}
