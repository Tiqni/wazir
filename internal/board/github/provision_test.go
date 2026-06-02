package github

import (
	"context"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// fakeAPI is a scriptable projectsAPI for orchestration tests.
type fakeAPI struct {
	project        projectInfo
	projectFound   bool
	created        bool
	updatedFieldID string
	updatedOpts    []optionInput
}

func (f *fakeAPI) OwnerID(ctx context.Context, ownerType, login string) (string, error) {
	return "OWNER", nil
}
func (f *fakeAPI) GetProject(ctx context.Context, ownerType, login string, number int) (projectInfo, bool, error) {
	return f.project, f.projectFound, nil
}
func (f *fakeAPI) GetProjectByID(ctx context.Context, projectID string) (projectInfo, error) {
	return f.project, nil
}
func (f *fakeAPI) CreateProject(ctx context.Context, ownerID, title string) (projectInfo, error) {
	f.created = true
	f.project = projectInfo{ProjectID: "P1", Number: 1, StatusFieldID: "F1",
		Options: []statusOption{{ID: "o-todo", Name: "Todo"}, {ID: "o-done", Name: "Done"}}}
	f.projectFound = true
	return f.project, nil
}
func (f *fakeAPI) UpdateStatusOptions(ctx context.Context, fieldID string, opts []optionInput) error {
	f.updatedFieldID = fieldID
	f.updatedOpts = opts
	// Simulate GitHub assigning ids to the newly-created options on re-read.
	var reread []statusOption
	for _, o := range opts {
		id := o.Name + "-id"
		if o.ID != nil {
			id = *o.ID
		}
		reread = append(reread, statusOption{ID: id, Name: o.Name, Color: o.Color})
	}
	f.project.Options = reread
	return nil
}
func (f *fakeAPI) FindItem(ctx context.Context, projectID, issueNodeID string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeAPI) SetItemStatus(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
	return nil
}
func (f *fakeAPI) ResolveIssue(ctx context.Context, issueNodeID string) (issueRef, error) {
	return issueRef{}, nil
}
func (f *fakeAPI) ListItems(ctx context.Context, projectID, statusFieldID, optionID string) ([]listedItem, error) {
	return nil, nil
}

func newTestBoard(api projectsAPI, st store.Store) *GitHubBoard {
	return &GitHubBoard{api: api, store: st, owner: "octocat", ownerType: "user", projectNumber: 1, boardName: "Wazir"}
}

func TestEnsureProvisionedCreatesAndCaches(t *testing.T) {
	api := &fakeAPI{projectFound: false}
	st := store.NewMemory()
	b := newTestBoard(api, st)

	err := b.EnsureProvisioned(context.Background(), board.BoardSpec{
		Name: "Wazir", Columns: board.AllPhases(), Create: true,
	})
	if err != nil {
		t.Fatalf("EnsureProvisioned: %v", err)
	}
	if !api.created {
		t.Error("expected CreateProject to be called")
	}
	rec, ok, _ := st.GetBoard("P1")
	if !ok {
		t.Fatal("board record not cached")
	}
	for _, p := range board.AllPhases() {
		if rec.Options[string(p)] == "" {
			t.Errorf("phase %s has no cached option id", p)
		}
	}
}

func TestEnsureProvisionedBootstrapMissingErrors(t *testing.T) {
	api := &fakeAPI{projectFound: false}
	b := newTestBoard(api, store.NewMemory())
	err := b.EnsureProvisioned(context.Background(), board.BoardSpec{
		Name: "Wazir", Columns: board.AllPhases(), Create: false,
	})
	if err == nil {
		t.Fatal("bootstrap of an absent board should error")
	}
	if api.created {
		t.Error("bootstrap must never create")
	}
}

func TestEnsureProvisionedIdempotent(t *testing.T) {
	api := &fakeAPI{
		projectFound: true,
		project:      projectInfo{ProjectID: "P1", Number: 1, StatusFieldID: "F1"},
	}
	// Pre-populate all columns so the second run is a no-op.
	for _, p := range board.AllPhases() {
		api.project.Options = append(api.project.Options, statusOption{ID: "id-" + string(p), Name: columnName(p)})
	}
	b := newTestBoard(api, store.NewMemory())
	ctx := context.Background()
	spec := board.BoardSpec{Name: "Wazir", Columns: board.AllPhases(), Create: true}

	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if api.updatedFieldID != "" {
		t.Error("no UpdateStatusOptions expected when columns already complete")
	}
}
