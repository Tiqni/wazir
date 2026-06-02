package github

import (
	"context"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func TestSplitRepoRejectsExtraSlash(t *testing.T) {
	for _, in := range []string{"owner/repo/extra", "owner", "/repo", "owner/", ""} {
		if _, _, err := splitRepo(in); err == nil {
			t.Errorf("splitRepo(%q) = nil error, want error", in)
		}
	}
	o, n, err := splitRepo("octocat/hello")
	if err != nil || o != "octocat" || n != "hello" {
		t.Errorf("splitRepo(octocat/hello) = %q,%q,%v", o, n, err)
	}
}

// A cold cache may hold only a ProjectItemID (set by MoveTo before the repo is
// known); resolving the card must merge in repo/number without losing the id.
func TestResolveCardPreservesProjectItemID(t *testing.T) {
	api := &fakeAPI{resolveRepo: "octocat/hello", resolveNumber: 9}
	st := store.NewMemory()
	st.PutCard("ISSUE1", store.CardRecord{ProjectItemID: "ITEM1"})
	b := &GitHubBoard{api: api, store: st}

	if _, err := b.resolveCard(context.Background(), "ISSUE1"); err != nil {
		t.Fatalf("resolveCard: %v", err)
	}
	rec, ok, _ := st.GetCard("ISSUE1")
	if !ok || rec.ProjectItemID != "ITEM1" {
		t.Errorf("ProjectItemID should be preserved, got %+v", rec)
	}
	if rec.Repo != "octocat/hello" || rec.IssueNumber != 9 {
		t.Errorf("repo/number not merged, got %+v", rec)
	}
}

func TestParseProjectItemAppliesAllowListFromCache(t *testing.T) {
	payload := loadFixture(t, "projects_v2_item.json") // content ISSUE_NODE_1, project PROJECT_NODE_1
	h := headersFor("projects_v2_item", "d7", sign([]byte("shh"), payload))

	// Cached repo NOT in the allow-list -> ignored.
	forbidden := newParser()
	forbidden.store = store.NewMemory()
	_ = forbidden.store.PutCard("ISSUE_NODE_1", store.CardRecord{Repo: "octocat/forbidden"})
	ev, err := forbidden.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("forbidden cached repo should be ignored, got %v", ev.Kind)
	}

	// Cached repo allowed -> phase change with Repo populated.
	allowed := newParser() // repos = ["octocat/hello"]
	allowed.store = store.NewMemory()
	_ = allowed.store.PutCard("ISSUE_NODE_1", store.CardRecord{Repo: "octocat/hello"})
	ev2, err := allowed.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev2.Kind != board.EventPhaseChanged || ev2.Repo != "octocat/hello" {
		t.Errorf("allowed cached repo: got kind=%v repo=%q", ev2.Kind, ev2.Repo)
	}
}
