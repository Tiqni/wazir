package github

import (
	"context"
	"testing"

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
