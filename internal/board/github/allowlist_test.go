package github

import (
	"context"
	"errors"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/store"
)

// resolveCard must refuse a card whose repo is not in the allow-list, so the
// write paths (PostComment/SetBody/GetCard) cannot act on out-of-scope repos.
func TestResolveCardRejectsForeignRepo(t *testing.T) {
	st := store.NewMemory()
	st.PutCard("ISSUE9", store.CardRecord{Repo: "octocat/forbidden", IssueNumber: 3})
	b := &GitHubBoard{store: st, repos: []string{"octocat/allowed"}}

	if _, err := b.resolveCard(context.Background(), "ISSUE9"); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("want ErrRepoNotAllowed, got %v", err)
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
