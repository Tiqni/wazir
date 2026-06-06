package github

import (
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func TestMergeAddsMissingPreservesExisting(t *testing.T) {
	existing := []statusOption{
		{ID: "o-todo", Name: "Todo", Color: "GRAY", Description: ""},
		{ID: "o-done", Name: "Done", Color: "GREEN", Description: "shipped"},
	}
	merged, changed := mergeStatusOptions(existing, board.AllPhases())

	if !changed {
		t.Fatal("expected changed=true (8 columns missing)")
	}
	// Existing options must be preserved by id, in place, unchanged.
	if merged[0].ID == nil || *merged[0].ID != "o-todo" {
		t.Errorf("first option lost its id: %+v", merged[0])
	}
	if merged[1].ID == nil || *merged[1].ID != "o-done" || merged[1].Description != "shipped" {
		t.Errorf("Done option not preserved: %+v", merged[1])
	}
	// New options for missing phases must be appended with no id.
	byName := map[string]optionInput{}
	for _, o := range merged {
		byName[o.Name] = o
	}
	inbox, ok := byName["Inbox"]
	if !ok || inbox.ID != nil {
		t.Errorf("Inbox should be appended without id: %+v ok=%v", inbox, ok)
	}
	if _, ok := byName["Awaiting Answers"]; !ok {
		t.Error("missing 'Awaiting Answers' column not added")
	}
}

func TestMergeIdempotentWhenComplete(t *testing.T) {
	var existing []statusOption
	for _, p := range board.AllPhases() {
		existing = append(existing, statusOption{ID: "id-" + string(p), Name: columnName(p), Color: optionColor(p)})
	}
	_, changed := mergeStatusOptions(existing, board.AllPhases())
	if changed {
		t.Error("expected changed=false when all columns already present")
	}
}
