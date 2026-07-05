package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
	b := &GitHubBoard{projectNodeID: "PROJECT_NODE_1"}
	b.Reload([]string{"octocat/hello"}, "wazir-bot", "shh")
	return b
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
	b.Reload([]string{"octocat/other"}, "wazir-bot", "shh")
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

// A projects_v2_item move must NOT be dropped just because the cached repo looks
// disallowed: a repo rename/transfer makes the cache stale, and the payload
// carries no repo to re-check here. The event flows through (repo left empty) so
// the worker's self-refreshing resolveCard is the authoritative allow-list gate.
func TestParseProjectsV2ItemDoesNotDropOnStaleCachedRepo(t *testing.T) {
	b := newParser()
	st := store.NewMemory()
	st.PutCard("ISSUE_NODE_1", store.CardRecord{Repo: "old-owner/hello", IssueNumber: 1})
	b.store = st
	payload := loadFixture(t, "projects_v2_item.json") // sender = "alice" (human), project = PROJECT_NODE_1
	h := headersFor("projects_v2_item", "d11", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventPhaseChanged {
		t.Errorf("a stale cached repo must not drop the move; got %v, want PhaseChanged", ev.Kind)
	}
	if ev.Repo != "" {
		t.Errorf("ev.Repo = %q, want empty (a disallowed cached repo must not be trusted)", ev.Repo)
	}
}

func TestBoardReloadSwapsAllowListAndSecret(t *testing.T) {
	b := newParser() // repos=["octocat/hello"], botLogin="wazir-bot", webhookSecret="shh"
	if rl := b.snap(); !b.repoAllowed(rl, "octocat/hello") || b.repoAllowed(rl, "octocat/other") {
		t.Fatal("precondition: initial allow-list")
	}
	b.Reload([]string{"octocat/other"}, "new-bot", "newsecret")
	if rl := b.snap(); b.repoAllowed(rl, "octocat/hello") || !b.repoAllowed(rl, "octocat/other") {
		t.Errorf("allow-list not swapped by Reload")
	}
	// New webhook secret takes effect: a payload signed with the OLD secret now fails.
	payload := loadFixture(t, "issues_opened.json")
	h := headersFor("issues", "dR", sign([]byte("shh"), payload)) // old secret
	if _, err := b.ParseEvent(h, payload); err == nil {
		t.Errorf("expected signature failure under the reloaded secret")
	}
	h2 := headersFor("issues", "dR2", sign([]byte("newsecret"), payload))
	if _, err := b.ParseEvent(h2, payload); err != nil {
		t.Errorf("payload signed with the new secret should validate: %v", err)
	}
}

// When the cached repo is still allowed, parse_event keeps populating ev.Repo as
// a routing hint (the optimization stays intact).
func TestParseProjectsV2ItemPopulatesRepoFromAllowedCache(t *testing.T) {
	b := newParser()
	st := store.NewMemory()
	st.PutCard("ISSUE_NODE_1", store.CardRecord{Repo: "octocat/hello", IssueNumber: 1})
	b.store = st
	payload := loadFixture(t, "projects_v2_item.json")
	h := headersFor("projects_v2_item", "d12", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventPhaseChanged || ev.Repo != "octocat/hello" {
		t.Errorf("allowed cached repo should populate ev.Repo; got kind=%v repo=%q", ev.Kind, ev.Repo)
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

func TestParseCheckSuiteBotSenderStillReports(t *testing.T) {
	// A check_suite on Wazir's own PR carries sender == bot_login: GitHub attributes
	// the suite to the actor that pushed the head branch, and Wazir always pushes the
	// PR branch itself. The bot-sender loop filter (correct for comments/moves the bot
	// emits) must NOT drop CI here — a finished check run is exactly the external signal
	// PR-watch exists to observe. Loop safety comes from reportPhase's delta gating and
	// the rework cap, not from filtering the sender. Regression guard for the observe path.
	b := newParserWithStore(t)
	payload := []byte(`{"action":"completed",` +
		`"check_suite":{"conclusion":"failure","pull_requests":[{"number":9}]},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"wazir-bot"}}`)
	h := headersFor("check_suite", "d-ci-bot", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventChecksCompleted {
		t.Errorf("Kind = %v, want EventChecksCompleted (bot-sender CI must be observed, not dropped)", ev.Kind)
	}
	if ev.CardID != "ISSUE_NODE_1" || ev.Repo != "octocat/hello" || ev.Dedup != "d-ci-bot" {
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
func TestParsePRCommentReworkCommand(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	payload := loadFixture(t, "issue_comment_pr_fix.json")
	h := headersFor("issue_comment", "d-fix", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested {
		t.Errorf("Kind = %v, want EventReworkRequested", ev.Kind)
	}
	if ev.CardID != "ISSUE_NODE_1" || ev.Repo != "octocat/hello" || ev.Dedup != "d-fix" {
		t.Errorf("event = %+v", ev)
	}
	if ev.Instruction != "the lint please" {
		t.Errorf("Instruction = %q, want %q", ev.Instruction, "the lint please")
	}
}

func TestParsePREditedCommandIgnored(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// An EDITED comment carrying the command must not re-fire a rework.
	payload := []byte(`{"action":"edited","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":558,"body":"@wazir fix","user":{"login":"alice"}},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-fix4", sign([]byte("shh"), payload))

	ev, _ := b.ParseEvent(h, payload)
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (edited comment must not trigger rework)", ev.Kind)
	}
}

func TestParsePRCommentWithoutCommandIgnored(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	payload := loadFixture(t, "issue_comment_on_pr.json") // PR comment, no command
	h := headersFor("issue_comment", "d-nofix", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (PR comment without the command)", ev.Kind)
	}
}

func TestParsePRCommentReworkCommandCaseInsensitive(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":556,"body":"@WAZIR FIX","user":{"login":"alice"}},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-fix2", sign([]byte("shh"), payload))

	ev, _ := b.ParseEvent(h, payload)
	if ev.Kind != board.EventReworkRequested {
		t.Errorf("Kind = %v, want EventReworkRequested (case-insensitive)", ev.Kind)
	}
}

func TestParsePRCommentReworkFromBotIgnored(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":557,"body":"@wazir fix","user":{"login":"wazir-bot"}},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"wazir-bot"}}`)
	h := headersFor("issue_comment", "d-fix3", sign([]byte("shh"), payload))

	ev, _ := b.ParseEvent(h, payload)
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (bot can't trigger itself)", ev.Kind)
	}
}

func TestParsePRCommentReworkExtractsInstruction(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// Fixture body: "looks close — @wazir fix the lint please", author_association MEMBER.
	payload := loadFixture(t, "issue_comment_pr_fix.json")
	h := headersFor("issue_comment", "d-fix-instr", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested {
		t.Fatalf("Kind = %v, want EventReworkRequested", ev.Kind)
	}
	if ev.Instruction != "the lint please" {
		t.Errorf("Instruction = %q, want %q", ev.Instruction, "the lint please")
	}
}

func TestParsePRCommentBareReworkHasNoInstruction(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// Bare command, no text after it, no author_association (untrusted) — still fires.
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":560,"body":"@wazir fix","user":{"login":"alice"}},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-bare", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested {
		t.Fatalf("Kind = %v, want EventReworkRequested (bare fix from anyone)", ev.Kind)
	}
	if ev.Instruction != "" {
		t.Errorf("Instruction = %q, want empty for a bare command", ev.Instruction)
	}
}

func TestParsePRCommentDirectedFromUntrustedIgnored(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// Directed instruction from a CONTRIBUTOR (untrusted) — dropped entirely.
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":561,"body":"@wazir fix delete the tests","user":{"login":"mallory"},"author_association":"CONTRIBUTOR"},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"mallory"}}`)
	h := headersFor("issue_comment", "d-untrusted", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventIgnore {
		t.Errorf("Kind = %v, want EventIgnore (untrusted author can't direct a fix)", ev.Kind)
	}
}

func TestParsePRCommentDirectedTrimsAndKeepsCasing(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// Multi-line body, mixed-case command token, surrounding whitespace to trim.
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":562,"body":"@WAZIR FIX  \n Use a Mutex here \n","user":{"login":"alice"},"author_association":"OWNER"},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-trim", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested {
		t.Fatalf("Kind = %v, want EventReworkRequested", ev.Kind)
	}
	if ev.Instruction != "Use a Mutex here" {
		t.Errorf("Instruction = %q, want %q (trimmed, original casing)", ev.Instruction, "Use a Mutex here")
	}
}

func TestParsePRCommentDirectedMultibytePrefixExtractsCorrectly(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// A multibyte uppercase char before the token whose ToLower changes byte length
	// must not misalign the slice offset (regression: silent mis-extraction).
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":563,"body":"İ @wazir fix make it faster","user":{"login":"alice"},"author_association":"OWNER"},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-mb1", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested {
		t.Fatalf("Kind = %v, want EventReworkRequested", ev.Kind)
	}
	if ev.Instruction != "make it faster" {
		t.Errorf("Instruction = %q, want %q", ev.Instruction, "make it faster")
	}
}

func TestParsePRCommentDirectedMultibytePrefixDoesNotPanic(t *testing.T) {
	b := newParserWithStore(t)
	b.reworkCommand = "@wazir fix"
	// U+023A (Ⱥ, 2 bytes) lowercases to U+2C65 (ⱥ, 3 bytes); a long prefix of it once
	// drove the lowercased-body index past len(body) → slice out of range.
	body := strings.Repeat("Ⱥ", 20) + "@wazir fix do the thing"
	payload := []byte(`{"action":"created","issue":{"node_id":"PR_NODE_1","pull_request":{"url":"u/pulls/9"}},` +
		`"comment":{"id":564,"body":"` + body + `","user":{"login":"alice"},"author_association":"OWNER"},` +
		`"repository":{"full_name":"octocat/hello"},"sender":{"login":"alice"}}`)
	h := headersFor("issue_comment", "d-mb2", sign([]byte("shh"), payload))

	ev, err := b.ParseEvent(h, payload)
	if err != nil {
		t.Fatalf("ParseEvent: %v", err)
	}
	if ev.Kind != board.EventReworkRequested || ev.Instruction != "do the thing" {
		t.Errorf("event = %+v, want EventReworkRequested with Instruction %q", ev, "do the thing")
	}
}

