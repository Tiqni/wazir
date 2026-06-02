package github

import (
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestColumnNameRoundTrip(t *testing.T) {
	for _, p := range board.AllPhases() {
		name := columnName(p)
		if name == "" {
			t.Errorf("columnName(%s) empty", p)
		}
		got, ok := phaseFromColumn(name)
		if !ok || got != p {
			t.Errorf("phaseFromColumn(%q) = %s,%v want %s", name, got, ok, p)
		}
	}
}

func TestColumnNameSpacing(t *testing.T) {
	if columnName(board.PhaseAwaitingAnswers) != "Awaiting Answers" {
		t.Errorf("got %q", columnName(board.PhaseAwaitingAnswers))
	}
	if columnName(board.PhasePRReview) != "PR Review" {
		t.Errorf("got %q", columnName(board.PhasePRReview))
	}
}

func TestEveryPhaseHasColor(t *testing.T) {
	for _, p := range board.AllPhases() {
		if optionColor(p) == "" {
			t.Errorf("no color for %s", p)
		}
	}
}
