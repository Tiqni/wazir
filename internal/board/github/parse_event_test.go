package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func sign(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return data
}

func newParser() *GitHubBoard {
	return &GitHubBoard{
		botLogin:      "wazir-bot",
		webhookSecret: "shh",
		projectNodeID: "PROJECT_NODE_1",
		repos:         []string{"octocat/hello"},
	}
}

func headersFor(event, delivery, sig string) map[string]string {
	return map[string]string{
		"X-GitHub-Event":      event,
		"X-GitHub-Delivery":   delivery,
		"X-Hub-Signature-256": sig,
	}
}

func TestParseIssueComment(t *testing.T) {
	b := newParser()
	payload := loadFixture(t, "issue_comment.json")
	h := headersFor("issue_comment", "d1", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventCommentAdded {
		t.Errorf("Kind = %v", ev.Kind)
	}
	if ev.CardID != "ISSUE_NODE_1" || ev.Repo != "octocat/hello" || ev.Dedup != "d1" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Comment == nil || ev.Comment.Author != "alice" || ev.Comment.IsBot {
		t.Errorf("comment = %+v", ev.Comment)
	}
}

func TestParseIssueOpened(t *testing.T) {
	b := newParser()
	payload := loadFixture(t, "issues_opened.json")
	h := headersFor("issues", "d2", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventCardCreated || ev.CardID != "ISSUE_NODE_1" {
		t.Errorf("event = %+v", ev)
	}
}

func TestParseProjectItemFiltersForeignProject(t *testing.T) {
	b := newParser()
	b.projectNodeID = "SOME_OTHER_PROJECT"
	payload := loadFixture(t, "projects_v2_item.json")
	h := headersFor("projects_v2_item", "d3", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("foreign project event should be ignored, got %v", ev.Kind)
	}
}

func TestParseRejectsBadSignature(t *testing.T) {
	b := newParser()
	payload := loadFixture(t, "issues_opened.json")
	h := headersFor("issues", "d4", "sha256=deadbeef")
	if _, err := b.ParseEvent(h, payload); err == nil {
		t.Fatal("expected signature validation error")
	}
}

func TestParseDropsForeignRepo(t *testing.T) {
	b := newParser()
	b.repos = []string{"octocat/other"}
	payload := loadFixture(t, "issues_opened.json")
	h := headersFor("issues", "d5", sign([]byte("shh"), payload))
	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("event from non-allow-listed repo should be ignored, got %v", ev.Kind)
	}
}

func TestParseProjectsV2ItemDropsBotSelfMove(t *testing.T) {
	b := newParser() // botLogin = "wazir-bot"
	payload := loadFixture(t, "projects_v2_item_bot.json")
	h := headersFor("projects_v2_item", "d9", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want Ignore (bot self-move loop prevention)", ev.Kind)
	}
}

func TestParseProjectsV2ItemKeepsHumanMove(t *testing.T) {
	b := newParser()
	payload := loadFixture(t, "projects_v2_item.json") // sender = "alice"
	h := headersFor("projects_v2_item", "d10", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventPhaseChanged || ev.CardID != "ISSUE_NODE_1" {
		t.Errorf("event = %+v, want PhaseChanged for a human move", ev)
	}
}

// newParserWithStore is newParser() plus a store seeded with a PR-index entry,
// so PR webhooks reverse-map to a card.
func newParserWithStore(t *testing.T) *GitHubBoard {
	t.Helper()
	b := newParser()
	st := store.NewMemory()
	if err := st.PutPRIndex("octocat/hello", 9, "ISSUE_NODE_1"); err != nil {
		t.Fatalf("seed PR-index: %v", err)
	}
	b.store = st
	return b
}

func TestParsePullRequestReviewChangesRequested(t *testing.T) {
	b := newParserWithStore(t)
	payload := loadFixture(t, "pull_request_review.json")
	h := headersFor("pull_request_review", "d-rev", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReviewSubmitted {
		t.Errorf("Kind = %v, want EventReviewSubmitted", ev.Kind)
	}
	if ev.CardID != "ISSUE_NODE_1" || ev.Repo != "octocat/hello" || ev.Dedup != "d-rev" {
		t.Errorf("event = %+v", ev)
	}
}

func TestParseCheckSuiteCompleted(t *testing.T) {
	b := newParserWithStore(t)
	payload := loadFixture(t, "check_suite.json")
	h := headersFor("check_suite", "d-ci", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventChecksCompleted {
		t.Errorf("Kind = %v, want EventChecksCompleted", ev.Kind)
	}
	if ev.CardID != "ISSUE_NODE_1" || ev.Repo != "octocat/hello" || ev.Dedup != "d-ci" {
		t.Errorf("event = %+v", ev)
	}
}

func TestParsePullRequestReviewCommentedIgnored(t *testing.T) {
	b := newParserWithStore(t)
	// A plain "commented" review is not decision-grade -> ignored.
	payload := []byte(`{"action":"submitted","review":{"state":"commented","user":{"login":"alice"}},` +
		`"pull_request":{"number":9},"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("pull_request_review", "d-rev3", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (commented is not decision-grade)", ev.Kind)
	}
}

func TestParsePullRequestReviewUnknownPRIgnored(t *testing.T) {
	b := newParser() // no PR-index entry
	b.store = store.NewMemory()
	payload := loadFixture(t, "pull_request_review.json")
	h := headersFor("pull_request_review", "d-rev2", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (no PR-index entry)", ev.Kind)
	}
}

func TestParseIssueCommentOnPRIgnored(t *testing.T) {
	b := newParserWithStore(t)
	payload := loadFixture(t, "issue_comment_on_pr.json")
	h := headersFor("issue_comment", "d-prc", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (comment on a PR, not the card issue)", ev.Kind)
	}
}
