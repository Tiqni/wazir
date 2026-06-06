package github

import (
	"context"
	"errors"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// pruneFakeProject is a board holding the 3 defaults plus Wazir's columns, so
// prune has Todo/In Progress to delete.
func pruneFakeProject() projectInfo {
	info := projectInfo{ProjectID: "P1", Number: 1, StatusFieldID: "F1", Options: []statusOption{
		{ID: "o-todo", Name: "Todo"},
		{ID: "o-inprog", Name: "In Progress"},
	}}
	for _, p := range board.AllPhases() {
		info.Options = append(info.Options, statusOption{ID: "id-" + string(p), Name: columnName(p)})
	}
	return info
}

func TestEnsureProvisionedPruneDeletesEmptyColumns(t *testing.T) {
	api := &fakeAPI{projectFound: true, project: pruneFakeProject()} // no itemCounts => all empty
	st := store.NewMemory()
	b := newTestBoard(api, st)
	spec := board.BoardSpec{Name: "Wazir", Columns: board.AllPhases(), Create: true, Prune: true}

	if err := b.EnsureProvisioned(context.Background(), spec); err != nil {
		t.Fatalf("EnsureProvisioned: %v", err)
	}
	// The update must have been sent with exactly Wazir's 9 columns (no Todo/In Progress).
	if len(api.updatedOpts) != len(board.AllPhases()) {
		t.Fatalf("sent %d options, want %d", len(api.updatedOpts), len(board.AllPhases()))
	}
	for _, o := range api.updatedOpts {
		if o.Name == "Todo" || o.Name == "In Progress" {
			t.Errorf("pruned set still contains %q", o.Name)
		}
	}
}

func TestEnsureProvisionedPruneRefusesOccupied(t *testing.T) {
	api := &fakeAPI{projectFound: true, project: pruneFakeProject(),
		itemCounts: map[string]int{"o-todo": 3}} // Todo holds 3 cards
	b := newTestBoard(api, store.NewMemory())
	spec := board.BoardSpec{Name: "Wazir", Columns: board.AllPhases(), Create: true, Prune: true}

	err := b.EnsureProvisioned(context.Background(), spec)
	if !errors.Is(err, ErrColumnsOccupied) {
		t.Fatalf("want ErrColumnsOccupied, got %v", err)
	}
	if api.updatedFieldID != "" {
		t.Error("must not send the update when a column is occupied")
	}
}

func TestEnsureProvisionedPruneForceDeletesOccupied(t *testing.T) {
	api := &fakeAPI{projectFound: true, project: pruneFakeProject(),
		itemCounts: map[string]int{"o-todo": 3}}
	b := newTestBoard(api, store.NewMemory())
	spec := board.BoardSpec{Name: "Wazir", Columns: board.AllPhases(), Create: true, Prune: true, Force: true}

	if err := b.EnsureProvisioned(context.Background(), spec); err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if api.updatedFieldID == "" {
		t.Error("force prune should have sent the update despite occupancy")
	}
}
