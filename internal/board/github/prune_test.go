package github

import (
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestPruneDropsExtrasAndCanonicalOrder(t *testing.T) {
	// A board provisioned additively: defaults first, then Wazir columns out of
	// canonical order (Done sits early). Prune should drop Todo/In Progress and
	// emit exactly Wazir's 10 in canonical order.
	existing := []statusOption{
		{ID: "o-todo", Name: "Todo", Color: "GRAY"},
		{ID: "o-inprog", Name: "In Progress", Color: "YELLOW"},
		{ID: "o-done", Name: "Done", Color: "GREEN", Description: "shipped"},
	}
	for _, p := range []board.Phase{
		board.PhaseInbox, board.PhaseBrainstorming, board.PhaseAwaitingAnswers,
		board.PhaseSpecReview, board.PhasePlanning, board.PhaseBuilding,
		board.PhasePRReview, board.PhaseFailed,
	} {
		existing = append(existing, statusOption{ID: "id-" + string(p), Name: columnName(p)})
	}

	merged, deleted, changed := pruneStatusOptions(existing, board.AllPhases())
	if !changed {
		t.Fatal("expected changed=true (deletions + reorder)")
	}

	// Deleted = the two non-Wazir defaults.
	delNames := map[string]bool{}
	for _, d := range deleted {
		delNames[d.Name] = true
	}
	if len(deleted) != 2 || !delNames["Todo"] || !delNames["In Progress"] {
		t.Errorf("deleted = %+v, want Todo + In Progress", deleted)
	}

	// merged is exactly Wazir's columns in canonical order.
	want := []string{"Inbox", "Brainstorming", "Awaiting Answers", "Spec Review",
		"Planning", "Building", "PR Review", "Reworking", "Done", "Failed"}
	got := optionNames(merged)
	if len(got) != len(want) {
		t.Fatalf("merged names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("merged[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Kept columns preserve their existing ids (Done by name-match, description too).
	byName := map[string]optionInput{}
	for _, o := range merged {
		byName[o.Name] = o
	}
	if done := byName["Done"]; done.ID == nil || *done.ID != "o-done" || done.Description != "shipped" {
		t.Errorf("Done not preserved by id: %+v", done)
	}
	if inbox := byName["Inbox"]; inbox.ID == nil || *inbox.ID != "id-Inbox" {
		t.Errorf("Inbox should keep its existing id: %+v", inbox)
	}
}

func TestPruneAddsMissingColumns(t *testing.T) {
	// A bare board (just defaults). Prune deletes Todo/In Progress, keeps Done,
	// and creates the 8 missing Wazir columns (no id).
	existing := []statusOption{
		{ID: "o-todo", Name: "Todo"},
		{ID: "o-inprog", Name: "In Progress"},
		{ID: "o-done", Name: "Done"},
	}
	merged, deleted, changed := pruneStatusOptions(existing, board.AllPhases())
	if !changed || len(deleted) != 2 {
		t.Fatalf("changed=%v deleted=%d, want true/2", changed, len(deleted))
	}
	byName := map[string]optionInput{}
	for _, o := range merged {
		byName[o.Name] = o
	}
	if inbox, ok := byName["Inbox"]; !ok || inbox.ID != nil {
		t.Errorf("Inbox should be created without id: %+v ok=%v", inbox, ok)
	}
	if done := byName["Done"]; done.ID == nil || *done.ID != "o-done" {
		t.Errorf("Done should keep its id: %+v", done)
	}
}

func TestPruneIdempotentWhenAlreadyExact(t *testing.T) {
	// Board already holds exactly Wazir's columns in canonical order.
	var existing []statusOption
	for _, p := range board.AllPhases() {
		existing = append(existing, statusOption{ID: "id-" + string(p), Name: columnName(p), Color: optionColor(p)})
	}
	_, deleted, changed := pruneStatusOptions(existing, board.AllPhases())
	if changed {
		t.Error("expected changed=false when board already matches exactly")
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions, got %+v", deleted)
	}
}
