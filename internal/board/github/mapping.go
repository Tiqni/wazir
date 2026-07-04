// Package github implements the board.Board port against GitHub Projects v2.
// Provider-specific mapping (column names, colors, ids) lives here, never in core.
package github

import "github.com/EmadMokhtar/wazir/internal/board"

// columnNames maps domain phases to GitHub Status option display names.
var columnNames = map[board.Phase]string{
	board.PhaseInbox:           "Inbox",
	board.PhaseBrainstorming:   "Brainstorming",
	board.PhaseAwaitingAnswers: "Awaiting Answers",
	board.PhaseSpecReview:      "Spec Review",
	board.PhasePlanning:        "Planning",
	board.PhaseBuilding:        "Building",
	board.PhasePRReview:        "PR Review",
	board.PhaseReworking:       "Reworking",
	board.PhaseDone:            "Done",
	board.PhaseFailed:          "Failed",
}

// optionColors assigns a Status option color per phase (required by the API).
var optionColors = map[board.Phase]string{
	board.PhaseInbox:           "GRAY",
	board.PhaseBrainstorming:   "PURPLE",
	board.PhaseAwaitingAnswers: "YELLOW",
	board.PhaseSpecReview:      "BLUE",
	board.PhasePlanning:        "ORANGE",
	board.PhaseBuilding:        "PINK",
	board.PhasePRReview:        "GREEN",
	board.PhaseReworking:       "PINK",
	board.PhaseDone:            "GREEN",
	board.PhaseFailed:          "RED",
}

func columnName(p board.Phase) string { return columnNames[p] }

func optionColor(p board.Phase) string {
	if c, ok := optionColors[p]; ok {
		return c
	}
	return "GRAY"
}

func phaseFromColumn(name string) (board.Phase, bool) {
	for p, n := range columnNames {
		if n == name {
			return p, true
		}
	}
	return "", false
}
