// Package board defines the Board port and its domain vocabulary.
// It must not import any provider package.
package board

import (
	"context"
	"time"
)

// Phase is a card's lifecycle column. Values are internal tokens; the
// display-name mapping lives inside a provider implementation.
type Phase string

const (
	PhaseInbox           Phase = "Inbox"
	PhaseBrainstorming   Phase = "Brainstorming"
	PhaseAwaitingAnswers Phase = "AwaitingAnswers"
	PhaseSpecReview      Phase = "SpecReview"
	PhasePlanning        Phase = "Planning"
	PhaseBuilding        Phase = "Building"
	PhasePRReview        Phase = "PRReview"
	PhaseDone            Phase = "Done"
	PhaseFailed          Phase = "Failed"
)

// AllPhases returns the §3 phases in board order.
func AllPhases() []Phase {
	return []Phase{
		PhaseInbox, PhaseBrainstorming, PhaseAwaitingAnswers, PhaseSpecReview,
		PhasePlanning, PhaseBuilding, PhasePRReview, PhaseDone, PhaseFailed,
	}
}

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool {
	for _, x := range AllPhases() {
		if x == p {
			return true
		}
	}
	return false
}

// Card is a board item and the work it tracks.
type Card struct {
	ID       string // opaque provider id (GitHub issue node id)
	Repo     string // "owner/name" — which forge repo this card targets
	Title    string
	Body     string
	Phase    Phase
	Comments []Comment
}

// Comment is one thread entry.
type Comment struct {
	ID      string
	Author  string
	IsBot   bool
	Body    string
	Created time.Time
}

// ApprovalSignal is how a human says "advance".
type ApprovalSignal int

const (
	SignalNone ApprovalSignal = iota
	SignalApproveSpec
	SignalRequestRevision
)

// EventKind classifies a normalized board event.
type EventKind int

const (
	EventIgnore EventKind = iota
	EventCardCreated
	EventCommentAdded
	EventPhaseChanged
	EventApprovalGiven
	EventReviewSubmitted // M5: a decision-grade PR review (approved | changes_requested) was submitted
	EventChecksCompleted // M5: a PR's check suite completed
)

// Event is a provider webhook normalized to domain vocabulary.
type Event struct {
	Kind     EventKind
	CardID   string
	Repo     string // "owner/name" (multi-repo routing, init-plan §4.1)
	Comment  *Comment
	NewPhase Phase
	Signal   ApprovalSignal
	Dedup    string // provider delivery id for idempotency
}

// BoardSpec describes a desired board for provisioning.
type BoardSpec struct {
	Name    string
	Columns []Phase
	Create  bool // true: create the board if absent (provision); false: bootstrap only
	Prune   bool // true: reconcile to EXACTLY Columns, deleting any other Status option
	Force   bool // with Prune: delete a column even if it still holds cards
}

// Board is the kanban control surface. The orchestrator depends only on this.
type Board interface {
	EnsureProvisioned(ctx context.Context, spec BoardSpec) error

	GetCard(ctx context.Context, cardID string) (Card, error)
	ListCards(ctx context.Context, phase Phase) ([]Card, error)

	PostComment(ctx context.Context, cardID, body string) error
	SetBody(ctx context.Context, cardID, markdown string) error
	MoveTo(ctx context.Context, cardID string, phase Phase) error

	ParseEvent(headers map[string]string, payload []byte) (Event, error)
}
