package github

import (
	"context"
	"errors"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/store"
)

// resolveCard must refuse a card whose repo is not in the allow-list, so the
// write paths (PostComment/SetBody/GetCard) cannot act on out-of-scope repos.
// Even after re-resolving from GitHub, a genuinely-foreign repo stays rejected.
func TestResolveCardRejectsForeignRepo(t *testing.T) {
	st := store.NewMemory()
	st.PutCard("ISSUE9", store.CardRecord{Repo: "octocat/forbidden", IssueNumber: 3})
	api := &fakeAPI{resolveRepo: "octocat/forbidden", resolveNumber: 3} // re-resolve still foreign
	b := &GitHubBoard{api: api, store: st, repos: []string{"octocat/allowed"}}

	if _, err := b.resolveCard(context.Background(), "ISSUE9"); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("want ErrRepoNotAllowed, got %v", err)
	}
}

// A repo rename/transfer leaves the cached owner/name stale. resolveCard must
// re-resolve from GitHub (not reject on the stale cache) and refresh the record,
// so a card whose repo moved into the allow-list starts working without a manual
// store reset.
func TestResolveCardReResolvesStaleCachedRepo(t *testing.T) {
	st := store.NewMemory()
	// Cached under the old owner (pre-transfer), now outside the allow-list.
	st.PutCard("ISSUE9", store.CardRecord{Repo: "old-owner/repo", IssueNumber: 3, ProjectItemID: "PVTI_1"})
	api := &fakeAPI{resolveRepo: "new-org/repo", resolveNumber: 3} // transferred → now allowed
	b := &GitHubBoard{api: api, store: st, repos: []string{"new-org/repo"}}

	ref, err := b.resolveCard(context.Background(), "ISSUE9")
	if err != nil {
		t.Fatalf("resolveCard should re-resolve a stale cached repo, got: %v", err)
	}
	if ref.Repo != "new-org/repo" || ref.Number != 3 {
		t.Errorf("ref = %+v, want the re-resolved new-org/repo #3", ref)
	}
	if !api.resolveCalled {
		t.Error("expected a re-resolve (ResolveIssue) on the stale/disallowed cached repo")
	}
	// The cache must be refreshed to the new repo (and preserve ProjectItemID).
	rec, _, _ := st.GetCard("ISSUE9")
	if rec.Repo != "new-org/repo" {
		t.Errorf("cached repo not refreshed: %q", rec.Repo)
	}
	if rec.ProjectItemID != "PVTI_1" {
		t.Errorf("ProjectItemID dropped during refresh: %q", rec.ProjectItemID)
	}
}

// Positive bot-detection case: a comment authored by BOT_LOGIN is flagged IsBot.
func TestParseIssueCommentDetectsBotByLogin(t *testing.T) {
	b := newParser()
	b.botLogin = "alice" // matches the fixture's comment author
	payload := loadFixture(t, "issue_comment.json")
	h := headersFor("issue_comment", "d6", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Comment == nil || !ev.Comment.IsBot {
		t.Errorf("expected IsBot=true when author==botLogin, got %+v", ev.Comment)
	}
}
