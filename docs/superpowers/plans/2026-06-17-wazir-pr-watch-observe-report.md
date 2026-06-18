# PR Watch — Observe + Report (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Once Wazir opens a PR and a card sits in PR Review, observe the PR's review + CI state from webhooks and report it back — always comment, move the card to Failed on changes-requested / red CI, leave green/approved informational.

**Architecture:** Webhook-triggered (`pull_request_review`, `check_suite`) with an authoritative REST re-fetch on each trigger. The `CodeForge` port gains a `PRStatus` read method; the `Board` port learns two new event kinds; PR→card mapping uses a new store PR-index; the worker gains an `ActReport` action whose `reportPhase` re-fetches state, applies delta suppression, comments, and conditionally moves to Failed.

**Tech Stack:** Go 1.25, `google/go-github/v66` (REST), `go.etcd.io/bbolt` (store), `go.uber.org/zap` (logging). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-17-wazir-pr-watch-observe-report-design.md`

**Branch:** `pr-watch-observe-report` (already created, off `m5-app-auth`).

---

## Conventions for every task

- Run `go build ./...` and `go test ./...` from repo root. Tests are network-free.
- `go test` reads ambient `WAZIR_*` env in some `config` tests; this plan touches no
  config tests, but if you see unrelated `config` failures, prefix with
  `env -u WAZIR_PROJECT_NUMBER -u WAZIR_PROJECT_OWNER -u WAZIR_GITHUB_OWNER_TYPE -u WAZIR_GITHUB_APP_ID -u WAZIR_GITHUB_INSTALLATION_ID -u WAZIR_GITHUB_PRIVATE_KEY -u WAZIR_GITHUB_WEBHOOK_SECRET`.
- This branch uses **plain** board fields (`botLogin`, `repos`, `webhookSecret`) — NOT
  the `atomic.Pointer` reload refactor (that's the separate `live-config-reload` branch).
  Do not introduce `Reload`/atomics here.
- `PostComment` already appends the `<!-- wazir -->` marker (board.go:224) — report
  comments are plain text; do not add the marker yourself.

---

## Task 1: Store — PR-index + CardRecord delta fields

**Files:**
- Modify: `internal/store/store.go` (CardRecord fields, Store interface, `prIndexKey`)
- Modify: `internal/store/bbolt.go` (bucket + `PutPRIndex`/`GetPRIndex`)
- Modify: `internal/store/memory.go` (map + `PutPRIndex`/`GetPRIndex`)
- Test: `internal/store/prindex_test.go` (new), `internal/store/bbolt_test.go` (reuse helpers)

- [ ] **Step 1: Write the failing test**

Create `internal/store/prindex_test.go`:

```go
package store

import (
	"path/filepath"
	"testing"
)

func openTempBbolt(t *testing.T) *Bbolt {
	t.Helper()
	s, err := OpenBbolt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenBbolt: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPRIndexRoundTripBbolt(t *testing.T) { runPRIndexSuite(t, openTempBbolt(t)) }
func TestPRIndexRoundTripMemory(t *testing.T) { runPRIndexSuite(t, NewMemory()) }

func runPRIndexSuite(t *testing.T, s Store) {
	t.Helper()
	// Miss before write.
	if _, ok, err := s.GetPRIndex("octocat/hello", 9); err != nil || ok {
		t.Fatalf("pre-write GetPRIndex: ok=%v err=%v, want ok=false", ok, err)
	}
	// Write then hit.
	if err := s.PutPRIndex("octocat/hello", 9, "ISSUE_NODE_1"); err != nil {
		t.Fatalf("PutPRIndex: %v", err)
	}
	id, ok, err := s.GetPRIndex("octocat/hello", 9)
	if err != nil || !ok || id != "ISSUE_NODE_1" {
		t.Fatalf("GetPRIndex = (%q, %v, %v), want (ISSUE_NODE_1, true, nil)", id, ok, err)
	}
	// Different PR number does not collide.
	if _, ok, _ := s.GetPRIndex("octocat/hello", 10); ok {
		t.Errorf("PR 10 should miss")
	}
}

func TestCardRecordDeltaFieldsPersist(t *testing.T) {
	s := openTempBbolt(t)
	rec := CardRecord{Repo: "octocat/hello", PRNumber: 9, LastReviewState: "changes_requested", LastCIConclusion: "failure"}
	if err := s.PutCard("ISSUE_NODE_1", rec); err != nil {
		t.Fatalf("PutCard: %v", err)
	}
	got, ok, err := s.GetCard("ISSUE_NODE_1")
	if err != nil || !ok {
		t.Fatalf("GetCard: ok=%v err=%v", ok, err)
	}
	if got.PRNumber != 9 || got.LastReviewState != "changes_requested" || got.LastCIConclusion != "failure" {
		t.Errorf("round-trip = %+v", got)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

Run: `go test ./internal/store/ -run 'TestPRIndex|TestCardRecordDelta' -v`
Expected: FAIL — `s.GetPRIndex undefined`, `CardRecord has no field PRNumber`.

- [ ] **Step 3: Add the CardRecord fields, interface methods, and key helper**

In `internal/store/store.go`, add to the `CardRecord` struct (after `Branch`):

```go
	PRNumber               int    // M5: open PR number; captured at OpenPR; enables PRStatus + PR-index
	LastReviewState        string // M5: "" | "approved" | "changes_requested" — report delta state
	LastCIConclusion       string // M5: "" | "success" | "failure" | "pending" — report delta state
```

In the same file, add to the `Store` interface (after `PutCard`):

```go
	// M5 — PR -> issue reverse index, so a PR webhook resolves to its card.
	PutPRIndex(repo string, prNumber int, issueNodeID string) error
	GetPRIndex(repo string, prNumber int) (issueNodeID string, ok bool, err error)
```

Add the shared key helper at the end of `internal/store/store.go` (and add `"strconv"` to its imports):

```go
// prIndexKey is the bbolt/memory key for the PR -> issue reverse index.
func prIndexKey(repo string, prNumber int) string {
	return repo + "#" + strconv.Itoa(prNumber)
}
```

- [ ] **Step 4: Implement in bbolt**

In `internal/store/bbolt.go`, add to the `var (...)` bucket block:

```go
	bucketPRIndex    = []byte("pr_index")
```

Add `bucketPRIndex` to the init slice in `OpenBbolt`:

```go
		for _, b := range [][]byte{bucketBoards, bucketCards, bucketDeliveries, bucketLocks, bucketPRIndex} {
```

Add the two methods (e.g. after `PutCard`):

```go
func (s *Bbolt) PutPRIndex(repo string, prNumber int, issueNodeID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPRIndex).Put([]byte(prIndexKey(repo, prNumber)), []byte(issueNodeID))
	})
}

func (s *Bbolt) GetPRIndex(repo string, prNumber int) (string, bool, error) {
	var id string
	var ok bool
	err := s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket(bucketPRIndex).Get([]byte(prIndexKey(repo, prNumber))); v != nil {
			id = string(v)
			ok = true
		}
		return nil
	})
	return id, ok, err
}
```

- [ ] **Step 5: Implement in memory**

In `internal/store/memory.go`, add a field to `Memory`:

```go
	prIndex    map[string]string
```

Initialize it in `NewMemory`:

```go
		prIndex:    map[string]string{},
```

Add the two methods:

```go
func (m *Memory) PutPRIndex(repo string, prNumber int, issueNodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prIndex[prIndexKey(repo, prNumber)] = issueNodeID
	return nil
}

func (m *Memory) GetPRIndex(repo string, prNumber int) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.prIndex[prIndexKey(repo, prNumber)]
	return id, ok, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (new tests + existing store tests).

- [ ] **Step 7: Build the whole module (the interface grew — confirm impls satisfy it)**

Run: `go build ./...`
Expected: builds clean. (No other package implements `Store` outside this package; the github board uses it via the interface and is unaffected at build time.)

- [ ] **Step 8: Commit**

```bash
git add internal/store/store.go internal/store/bbolt.go internal/store/memory.go internal/store/prindex_test.go
git commit -m "feat(store): PR-index reverse map + CardRecord PR/CI delta fields"
```

---

## Task 2: Forge — `PRStatus` type + read method (additive)

**Files:**
- Modify: `internal/forge/forge.go` (PRStatus struct + interface method)
- Modify: `internal/forge/github/forge.go` (REST implementation)
- Modify: `internal/orchestrator/worker_test.go` (fakeForge gains a `PRStatus` stub — required so the orchestrator test package still compiles once the interface grows)
- Test: `internal/forge/github/prstatus_test.go` (new)

> This task is **additive** — it does NOT change `OpenPR`. (That happens in Task 4.)
> Adding a method to `CodeForge` breaks every implementor at compile time, so the
> real impl and the test fakeForge are both updated here.

- [ ] **Step 1: Write the failing test**

Create `internal/forge/github/prstatus_test.go`:

```go
package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"
)

// prStatusServer stubs the three REST endpoints PRStatus calls, keyed by path.
func prStatusServer(t *testing.T, prBody, reviewsBody, checkRunsBody string) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			w.Write([]byte(reviewsBody))
		case strings.Contains(r.URL.Path, "/check-runs"):
			w.Write([]byte(checkRunsBody))
		case strings.Contains(r.URL.Path, "/pulls/"):
			w.Write([]byte(prBody))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := github.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	c.BaseURL = u
	return c
}

const prHeadSHA = `{"number":9,"head":{"sha":"abc123"}}`

func TestPRStatusChangesRequestedWinsLatest(t *testing.T) {
	// alice approved then later requested changes -> latest counts.
	reviews := `[
		{"user":{"login":"alice"},"state":"APPROVED","submitted_at":"2026-06-17T10:00:00Z"},
		{"user":{"login":"alice"},"state":"CHANGES_REQUESTED","submitted_at":"2026-06-17T11:00:00Z"}
	]`
	checks := `{"total_count":1,"check_runs":[{"name":"build","status":"completed","conclusion":"success"}]}`
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, reviews, checks)}

	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.ReviewDecision != "changes_requested" {
		t.Errorf("ReviewDecision = %q, want changes_requested", st.ReviewDecision)
	}
	if st.CIConclusion != "success" {
		t.Errorf("CIConclusion = %q, want success", st.CIConclusion)
	}
	if st.HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %q", st.HeadSHA)
	}
}

func TestPRStatusCIFailureCollectsNames(t *testing.T) {
	reviews := `[{"user":{"login":"bob"},"state":"APPROVED","submitted_at":"2026-06-17T10:00:00Z"}]`
	checks := `{"total_count":2,"check_runs":[
		{"name":"lint","status":"completed","conclusion":"failure"},
		{"name":"unit","status":"completed","conclusion":"success"}
	]}`
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, reviews, checks)}

	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.ReviewDecision != "approved" {
		t.Errorf("ReviewDecision = %q, want approved", st.ReviewDecision)
	}
	if st.CIConclusion != "failure" {
		t.Errorf("CIConclusion = %q, want failure", st.CIConclusion)
	}
	if len(st.FailingChecks) != 1 || st.FailingChecks[0] != "lint" {
		t.Errorf("FailingChecks = %v, want [lint]", st.FailingChecks)
	}
}

func TestPRStatusInProgressIsPending(t *testing.T) {
	reviews := `[]`
	checks := `{"total_count":1,"check_runs":[{"name":"build","status":"in_progress","conclusion":""}]}`
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, reviews, checks)}

	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.CIConclusion != "pending" {
		t.Errorf("CIConclusion = %q, want pending", st.CIConclusion)
	}
	if st.ReviewDecision != "" {
		t.Errorf("ReviewDecision = %q, want empty", st.ReviewDecision)
	}
}

func TestPRStatusNoChecksIsEmpty(t *testing.T) {
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, `[]`, `{"total_count":0,"check_runs":[]}`)}
	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.CIConclusion != "" {
		t.Errorf("CIConclusion = %q, want empty (no checks)", st.CIConclusion)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

Run: `go test ./internal/forge/github/ -run TestPRStatus -v`
Expected: FAIL — `f.PRStatus undefined`.

- [ ] **Step 3: Add the domain type + interface method**

In `internal/forge/forge.go`, add the struct (above the interface) and the method (inside `CodeForge`, after `OpenPR`):

```go
// PRStatus is the observed review + CI state of a pull request. Values are
// domain tokens; no provider types cross this port.
type PRStatus struct {
	ReviewDecision string   // "approved" | "changes_requested" | "review_required" | ""
	CIConclusion   string   // "success" | "failure" | "pending" | ""  ("" = no checks present)
	FailingChecks  []string // names of failed check-runs, for the report comment
	HeadSHA        string   // the commit the checks ran against
}
```

```go
	// PRStatus reports the current review decision + CI conclusion for a PR.
	PRStatus(ctx context.Context, repo string, prNumber int) (PRStatus, error)
```

- [ ] **Step 4: Implement it in the github forge**

In `internal/forge/github/forge.go`, add (after `OpenPR`, before the `var _ forge.CodeForge` line):

```go
// PRStatus fetches the PR's head SHA, reduces its reviews to a single decision,
// and reduces its check-runs to a single CI conclusion. Provider types stay here.
func (f *GitHubForge) PRStatus(ctx context.Context, repo string, prNumber int) (forge.PRStatus, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return forge.PRStatus{}, err
	}
	pr, _, err := f.rest.PullRequests.Get(ctx, owner, name, prNumber)
	if err != nil {
		return forge.PRStatus{}, fmt.Errorf("get pr: %w", err)
	}
	headSHA := pr.GetHead().GetSHA()

	reviews, _, err := f.rest.PullRequests.ListReviews(ctx, owner, name, prNumber, &github.ListOptions{PerPage: 100})
	if err != nil {
		return forge.PRStatus{}, fmt.Errorf("list reviews: %w", err)
	}

	runs, _, err := f.rest.Checks.ListCheckRunsForRef(ctx, owner, name, headSHA, &github.ListCheckRunsOptions{ListOptions: github.ListOptions{PerPage: 100}})
	if err != nil {
		return forge.PRStatus{}, fmt.Errorf("list check-runs: %w", err)
	}

	review := reduceReviewDecision(reviews)
	ci, failing := reduceCIConclusion(runs)
	return forge.PRStatus{
		ReviewDecision: review,
		CIConclusion:   ci,
		FailingChecks:  failing,
		HeadSHA:        headSHA,
	}, nil
}

// reduceReviewDecision mirrors GitHub's rule: only a reviewer's latest
// APPROVED / CHANGES_REQUESTED counts (COMMENTED / DISMISSED are ignored).
// Any latest CHANGES_REQUESTED => changes_requested; else any APPROVED =>
// approved; else "" (review_required, treated as no decision).
func reduceReviewDecision(reviews []*github.PullRequestReview) string {
	latest := map[string]string{} // login -> latest decisive state (uppercase)
	for _, r := range reviews {
		state := r.GetState()
		if state != "APPROVED" && state != "CHANGES_REQUESTED" {
			continue
		}
		latest[r.GetUser().GetLogin()] = state // reviews arrive chronologically
	}
	approved := false
	for _, s := range latest {
		if s == "CHANGES_REQUESTED" {
			return "changes_requested"
		}
		if s == "APPROVED" {
			approved = true
		}
	}
	if approved {
		return "approved"
	}
	return ""
}

// reduceCIConclusion: no runs => ""; any run not completed => "pending";
// else any failing conclusion => "failure" (+ names); else "success".
func reduceCIConclusion(runs *github.ListCheckRunsResults) (string, []string) {
	if runs == nil || runs.GetTotal() == 0 {
		return "", nil
	}
	var failing []string
	pending := false
	for _, run := range runs.CheckRuns {
		if run.GetStatus() != "completed" {
			pending = true
			continue
		}
		switch run.GetConclusion() {
		case "failure", "timed_out", "cancelled", "action_required":
			failing = append(failing, run.GetName())
		}
	}
	if len(failing) > 0 {
		return "failure", failing
	}
	if pending {
		return "pending", nil
	}
	return "success", nil
}
```

- [ ] **Step 5: Add a `PRStatus` stub to the orchestrator test fakeForge (keep its package compiling)**

In `internal/orchestrator/worker_test.go`, add two fields to `fakeForge`:

```go
	prStatus    forge.PRStatus
	prStatusErr error
```

Add the method (after `OpenPR`, before the `var _ forge.CodeForge` line):

```go
func (f *fakeForge) PRStatus(ctx context.Context, repo string, prNumber int) (forge.PRStatus, error) {
	f.calls = append(f.calls, "prStatus")
	return f.prStatus, f.prStatusErr
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/forge/github/ -run TestPRStatus -v && go test ./internal/orchestrator/`
Expected: PASS (forge PRStatus tests green; orchestrator package compiles + passes).

- [ ] **Step 7: Commit**

```bash
git add internal/forge/forge.go internal/forge/github/forge.go internal/forge/github/prstatus_test.go internal/orchestrator/worker_test.go
git commit -m "feat(forge): PRStatus read method (reviews + check-runs reduction)"
```

---

## Task 3: Board — new event kinds + ParseEvent for PR review & check suite

**Files:**
- Modify: `internal/board/board.go` (two new `EventKind` constants)
- Modify: `internal/board/github/parse_event.go` (two new webhook cases + PR-index lookup + issue_comment-on-PR guard)
- Test: `internal/board/github/parse_event_test.go` (new cases)
- Create: `internal/board/github/testdata/pull_request_review.json`, `internal/board/github/testdata/check_suite.json`, `internal/board/github/testdata/issue_comment_on_pr.json`

- [ ] **Step 1: Add the test fixtures**

Create `internal/board/github/testdata/pull_request_review.json`:

```json
{
  "action": "submitted",
  "review": { "state": "changes_requested", "user": { "login": "alice" } },
  "pull_request": { "number": 9 },
  "repository": { "full_name": "octocat/hello" },
  "sender": { "login": "alice" }
}
```

Create `internal/board/github/testdata/check_suite.json`:

```json
{
  "action": "completed",
  "check_suite": { "conclusion": "failure", "pull_requests": [ { "number": 9 } ] },
  "repository": { "full_name": "octocat/hello" },
  "sender": { "login": "github-actions[bot]" }
}
```

Create `internal/board/github/testdata/issue_comment_on_pr.json` (an `issue_comment` whose issue is a PR — `pull_request` present):

```json
{
  "action": "created",
  "issue": { "node_id": "PR_NODE_1", "pull_request": { "url": "https://api.github.com/repos/octocat/hello/pulls/9" } },
  "comment": { "id": 123, "body": "the linter is failing", "user": { "login": "alice" } },
  "repository": { "full_name": "octocat/hello" },
  "sender": { "login": "alice" }
}
```

- [ ] **Step 2: Write the failing tests**

Append to `internal/board/github/parse_event_test.go`:

```go
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
	if ev.CardID != "ISSUE_NODE_1" {
		t.Errorf("CardID = %q, want ISSUE_NODE_1", ev.CardID)
	}
}

func TestParsePullRequestReviewUnknownPRIgnored(t *testing.T) {
	b := newParser() // no store / no PR-index entry
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
```

Add `"github.com/EmadMokhtar/wazir/internal/store"` to the test file's imports if not present.

- [ ] **Step 3: Run them to confirm they fail**

Run: `go test ./internal/board/github/ -run 'PullRequestReview|CheckSuite|IssueCommentOnPR' -v`
Expected: FAIL — `EventReviewSubmitted` undefined; the new cases don't exist yet.

- [ ] **Step 4: Add the new event kinds**

In `internal/board/board.go`, extend the `EventKind` constants:

```go
const (
	EventIgnore EventKind = iota
	EventCardCreated
	EventCommentAdded
	EventPhaseChanged
	EventApprovalGiven
	EventReviewSubmitted // M5: a decision-grade PR review (approved | changes_requested) was submitted
	EventChecksCompleted // M5: a PR's check suite completed
)
```

- [ ] **Step 5: Implement the parser cases + guard**

In `internal/board/github/parse_event.go`:

(a) Add the PR-index lookup helper (e.g. below `repoAllowed`):

```go
// lookupPRIndex resolves a PR number to its card's issue node id via the store
// reverse index. Returns ("", false) on a cold index or a missing store.
func (b *GitHubBoard) lookupPRIndex(repo string, prNumber int) (string, bool) {
	if b.store == nil {
		return "", false
	}
	id, ok, err := b.store.GetPRIndex(repo, prNumber)
	if err != nil || !ok {
		return "", false
	}
	return id, true
}
```

(b) Inside the `case *github.IssueCommentEvent:` block, add this guard as the first
statement (before the existing `repo := ...`):

```go
		// A conversation comment on a PR arrives as issue_comment, but its issue
		// node id is the PR's, not the card's. Ignore here (phase-2 rework will
		// route these via the PR-index). See the phase-1 design.
		if e.GetIssue().IsPullRequest() {
			return board.Event{Kind: board.EventIgnore}, nil
		}
```

(c) Add two new cases to the `switch e := raw.(type)` (e.g. after the `*github.ProjectV2ItemEvent` case, before `default:`):

```go
	case *github.PullRequestReviewEvent:
		if e.GetAction() != "submitted" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		// Decision-grade only: ignore "commented"/"dismissed" reviews.
		if s := e.GetReview().GetState(); s != "approved" && s != "changes_requested" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		if b.botLogin != "" && e.GetSender().GetLogin() == b.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		cardID, ok := b.lookupPRIndex(repo, e.GetPullRequest().GetNumber())
		if !ok {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		return board.Event{Kind: board.EventReviewSubmitted, CardID: cardID, Repo: repo, Dedup: delivery}, nil

	case *github.CheckSuiteEvent:
		if e.GetAction() != "completed" {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		repo := e.GetRepo().GetFullName()
		if !b.repoAllowed(repo) {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		if b.botLogin != "" && e.GetSender().GetLogin() == b.botLogin {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		prs := e.GetCheckSuite().PullRequests
		if len(prs) == 0 {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		cardID, ok := b.lookupPRIndex(repo, prs[0].GetNumber())
		if !ok {
			return board.Event{Kind: board.EventIgnore}, nil
		}
		return board.Event{Kind: board.EventChecksCompleted, CardID: cardID, Repo: repo, Dedup: delivery}, nil
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/board/github/ -v`
Expected: PASS (new cases + all existing ParseEvent tests).

- [ ] **Step 7: Build + commit**

Run: `go build ./...`
Expected: clean.

```bash
git add internal/board/board.go internal/board/github/parse_event.go internal/board/github/parse_event_test.go internal/board/github/testdata/pull_request_review.json internal/board/github/testdata/check_suite.json internal/board/github/testdata/issue_comment_on_pr.json
git commit -m "feat(board): parse pull_request_review + check_suite; ignore PR issue_comments"
```

---

## Task 4: Worker — capture PR number at open, persist it + the PR-index

**Files:**
- Modify: `internal/forge/forge.go` (`OpenPR` signature)
- Modify: `internal/forge/github/forge.go` (`OpenPR` returns the number)
- Modify: `internal/forge/github/forge_test.go` (existing OpenPR test call)
- Modify: `internal/orchestrator/worker.go` (`executePhase` captures + persists)
- Modify: `internal/orchestrator/worker_test.go` (fakeForge `OpenPR` signature + a persist assertion)

- [ ] **Step 1: Write/adjust the failing test**

Add to `internal/orchestrator/worker_test.go` a focused execute test asserting persistence. (The resolver routes any `PhaseBuilding` event to `ActExecute`, and `execute`'s `ActExecute` case reads the worktree/branch/plan from the seeded record, so a seeded `PhaseBuilding` card drives straight into `executePhase`.)

```go
func TestExecutePersistsPRNumberAndIndex(t *testing.T) {
	ctx := context.Background()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhaseBuilding})
	st := store.NewMemory()
	// A Building re-entry path reads worktree/branch/plan from the record.
	st.PutCard("I1", store.CardRecord{Repo: "octocat/hello", IssueNumber: 41, WorktreePath: "/wt", Branch: "feature/issue-41-t", PlanPath: "/wt/plan.md"})
	brain := &scriptedBrain{execute: []ExecuteResult{{Status: StatusComplete, Notes: "done", TestSummary: "ok"}}}
	ff := &fakeForge{prURL: "https://github.com/octocat/hello/pull/9", prNumber: 9, wtPath: "/wt"}
	w := NewWorker(b, ff, brain, st, nil)

	if err := w.Process(ctx, board.Event{Kind: board.EventPhaseChanged, CardID: "I1", NewPhase: board.PhaseBuilding}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.PRNumber != 9 {
		t.Errorf("CardRecord.PRNumber = %d, want 9", rec.PRNumber)
	}
	id, ok, _ := st.GetPRIndex("octocat/hello", 9)
	if !ok || id != "I1" {
		t.Errorf("PR-index = (%q, %v), want (I1, true)", id, ok)
	}
}
```

- [ ] **Step 2: Run it to confirm it fails to compile**

Run: `go test ./internal/orchestrator/ -run TestExecutePersistsPRNumberAndIndex -v`
Expected: FAIL — `fakeForge` has no `prNumber` field / `OpenPR` returns 2 values.

- [ ] **Step 3: Change the `OpenPR` port signature**

In `internal/forge/forge.go`, change the interface method:

```go
	// OpenPR opens a pull request and returns its URL and number.
	OpenPR(ctx context.Context, repo, branch, base, title, body string) (prURL string, prNumber int, err error)
```

- [ ] **Step 4: Update the github forge impl**

In `internal/forge/github/forge.go`, change `OpenPR`:

```go
// OpenPR opens a pull request and returns its HTML URL and number.
func (f *GitHubForge) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, int, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", 0, err
	}
	pr, _, err := f.rest.PullRequests.Create(ctx, owner, name, &github.NewPullRequest{
		Title: &title,
		Head:  &branch,
		Base:  &base,
		Body:  &body,
	})
	if err != nil {
		return "", 0, fmt.Errorf("create pr: %w", err)
	}
	return pr.GetHTMLURL(), pr.GetNumber(), nil
}
```

- [ ] **Step 5: Update the existing OpenPR forge test**

In `internal/forge/github/forge_test.go`, change the call + add a number assertion. Make the stub response include a number:

```go
		w.Write([]byte(`{"number":9,"html_url":"https://github.com/octocat/hello/pull/9"}`))
```

```go
	prURL, prNumber, err := f.OpenPR(context.Background(), "octocat/hello", "feature/x", "main", "Add X", "body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if prURL != "https://github.com/octocat/hello/pull/9" {
		t.Errorf("prURL = %q", prURL)
	}
	if prNumber != 9 {
		t.Errorf("prNumber = %d, want 9", prNumber)
	}
```

- [ ] **Step 6: Update the orchestrator fakeForge `OpenPR`**

In `internal/orchestrator/worker_test.go`, change the `fakeForge.OpenPR` method:

```go
func (f *fakeForge) OpenPR(ctx context.Context, repo, branch, base, title, body string) (string, int, error) {
	f.calls = append(f.calls, "openPR")
	return f.prURL, f.prNumber, nil
}
```

(`prNumber int` is already a field if Task 2 was followed; if not, add `prNumber int` to the struct.)

- [ ] **Step 7: Capture + persist in `executePhase`**

In `internal/orchestrator/worker.go`, replace the OpenPR block in `executePhase`:

```go
	url, prNumber, err := w.forge.OpenPR(ctx, card.Repo, branch, w.base, card.Title, prBody(res))
	if err != nil {
		return fmt.Errorf("open pr: %w", err)
	}
	// Persist PR identity so the observe+report phase (and phase-2 rework) can
	// resolve this PR back to the card: the number on the record + the
	// repo#pr -> issue reverse index ParseEvent reads.
	rec, _, err := w.store.GetCard(card.ID)
	if err != nil {
		return fmt.Errorf("read card record %s: %w", card.ID, err)
	}
	rec.PRNumber = prNumber
	if err := w.store.PutCard(card.ID, rec); err != nil {
		return fmt.Errorf("persist pr number: %w", err)
	}
	if err := w.store.PutPRIndex(card.Repo, prNumber, card.ID); err != nil {
		return fmt.Errorf("persist pr index: %w", err)
	}
	if err := w.board.PostComment(ctx, card.ID, "Opened PR: "+url); err != nil {
		return err
	}
	if err := w.board.MoveTo(ctx, card.ID, board.PhasePRReview); err != nil {
		return err
	}
```

- [ ] **Step 8: Run the full module tests**

Run: `go build ./... && go test ./...`
Expected: PASS. (`cmd/wazir/serve.go` calls `OpenPR` only through the port via the
worker, not directly, so no CLI change is needed. If `go build` flags a direct
`OpenPR` caller anywhere, update it to the 3-value form.)

- [ ] **Step 9: Commit**

```bash
git add internal/forge/forge.go internal/forge/github/forge.go internal/forge/github/forge_test.go internal/orchestrator/worker.go internal/orchestrator/worker_test.go
git commit -m "feat(forge,worker): OpenPR returns PR number; persist number + PR-index"
```

---

## Task 5: Orchestrator — `ActReport` + resolver routing + `reportPhase`

**Files:**
- Modify: `internal/orchestrator/decision.go` (`ActReport` + `String()`)
- Modify: `internal/orchestrator/resolver.go` (`PhasePRReview` routing)
- Modify: `internal/orchestrator/worker.go` (`execute` case + `reportPhase` + `reportComment`)
- Modify: `internal/orchestrator/resolver_test.go` (new table rows)
- Test: `internal/orchestrator/report_test.go` (new)

- [ ] **Step 1: Write the failing resolver rows**

In `internal/orchestrator/resolver_test.go`, add to the `cases` slice (and update the
existing `"prreview -> none"` row to keep it — a bare `EventPhaseChanged` in PRReview
still resolves to None):

```go
		{"prreview review submitted -> report", board.PhasePRReview, board.Event{Kind: board.EventReviewSubmitted}, "", ActReport},
		{"prreview checks completed -> report", board.PhasePRReview, board.Event{Kind: board.EventChecksCompleted}, "", ActReport},
```

- [ ] **Step 2: Write the failing reportPhase tests**

Create `internal/orchestrator/report_test.go`:

```go
package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	memboard "github.com/EmadMokhtar/wazir/internal/board/memory"
	"github.com/EmadMokhtar/wazir/internal/forge"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func reportSetup(t *testing.T, status forge.PRStatus, statusErr error, last store.CardRecord) (*memboard.Board, *store.Memory, *fakeForge, *Worker) {
	t.Helper()
	b := memboard.New()
	b.Seed(board.Card{ID: "I1", Repo: "octocat/hello", Title: "t", Phase: board.PhasePRReview})
	st := store.NewMemory()
	last.Repo = "octocat/hello"
	if last.PRNumber == 0 {
		last.PRNumber = 9
	}
	st.PutCard("I1", last)
	ff := &fakeForge{prStatus: status, prStatusErr: statusErr}
	w := NewWorker(b, ff, &scriptedBrain{}, st, nil)
	return b, st, ff, w
}

func process(t *testing.T, w *Worker, kind board.EventKind) {
	t.Helper()
	if err := w.Process(context.Background(), board.Event{Kind: kind, CardID: "I1"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
}

func TestReportChangesRequestedMovesToFailed(t *testing.T) {
	b, st, _, w := reportSetup(t, forge.PRStatus{ReviewDecision: "changes_requested"}, nil, store.CardRecord{})
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "Changes requested") {
		t.Errorf("comment = %+v", card.Comments)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "changes_requested" {
		t.Errorf("LastReviewState = %q", rec.LastReviewState)
	}
}

func TestReportCIFailureMovesToFailedWithNames(t *testing.T) {
	b, _, _, w := reportSetup(t, forge.PRStatus{CIConclusion: "failure", FailingChecks: []string{"lint", "unit"}}, nil, store.CardRecord{})
	process(t, w, board.EventChecksCompleted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhaseFailed {
		t.Errorf("phase = %q, want Failed", card.Phase)
	}
	if len(card.Comments) != 1 || !strings.Contains(card.Comments[0].Body, "lint") {
		t.Errorf("comment should name failing checks: %+v", card.Comments)
	}
}

func TestReportApprovedDoesNotMove(t *testing.T) {
	b, _, _, w := reportSetup(t, forge.PRStatus{ReviewDecision: "approved"}, nil, store.CardRecord{})
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview (approved is informational)", card.Phase)
	}
	if len(card.Comments) != 1 {
		t.Errorf("want 1 informational comment, got %d", len(card.Comments))
	}
}

func TestReportDeltaSuppressesRepeat(t *testing.T) {
	// First report records changes_requested; the card moves to Failed.
	b, st, ff, w := reportSetup(t, forge.PRStatus{ReviewDecision: "changes_requested"}, nil, store.CardRecord{LastReviewState: "changes_requested"})
	_ = st
	process(t, w, board.EventReviewSubmitted)

	card, _ := b.GetCard(context.Background(), "I1")
	// Same state as the persisted last state -> no comment, no move stays in PRReview.
	if len(card.Comments) != 0 {
		t.Errorf("delta should suppress the comment; got %+v", card.Comments)
	}
	if card.Phase != board.PhasePRReview {
		t.Errorf("phase = %q, want PRReview (no change => no move)", card.Phase)
	}
	if !contains(ff.calls, "prStatus") {
		t.Errorf("PRStatus should still be fetched; calls=%v", ff.calls)
	}
}

func TestReportReadErrorIsSoft(t *testing.T) {
	b, st, _, w := reportSetup(t, forge.PRStatus{}, errors.New("boom"), store.CardRecord{})
	process(t, w, board.EventChecksCompleted) // must return nil (no fail())

	card, _ := b.GetCard(context.Background(), "I1")
	if card.Phase != board.PhasePRReview {
		t.Errorf("a read error must not move the card; phase = %q", card.Phase)
	}
	if len(card.Comments) != 0 {
		t.Errorf("a read error must not comment; got %+v", card.Comments)
	}
	rec, _, _ := st.GetCard("I1")
	if rec.LastReviewState != "" || rec.LastCIConclusion != "" {
		t.Errorf("a read error must not write delta state; rec=%+v", rec)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
```

> `scriptedBrain{}` (all fields zero-valued) is fine here — `reportPhase` never calls
> the brain; `NewWorker` only stores it.

- [ ] **Step 3: Run them to confirm they fail**

Run: `go test ./internal/orchestrator/ -run 'Resolver|Report' -v`
Expected: FAIL — `ActReport` undefined; `reportPhase` not wired.

- [ ] **Step 4: Add the `ActReport` action**

In `internal/orchestrator/decision.go`, add to the const block (after `ActExecute`):

```go
	ActReport // PRReview: observe PR review/CI state and report it
```

Add to `String()` (before `default`):

```go
	case ActReport:
		return "Report"
```

- [ ] **Step 5: Route it in the resolver**

In `internal/orchestrator/resolver.go`, add an explicit `PhasePRReview` case before the
`default` (which still covers Done/Failed):

```go
	case board.PhasePRReview:
		if ev.Kind == board.EventReviewSubmitted || ev.Kind == board.EventChecksCompleted {
			return Decision{ActReport}
		}
		return Decision{ActNone}
```

- [ ] **Step 6: Wire the worker action + implement `reportPhase`**

In `internal/orchestrator/worker.go`, add a case to the `execute` switch (after the
`ActExecute` case):

```go
	case ActReport:
		return w.reportPhase(ctx, card)
```

Add the implementation (e.g. after `executePhase`):

```go
// reportPhase observes the PR's current review + CI state and reports it on the
// card. Read failures are SOFT (logged, no comment, no move, no delta write) —
// the error is ours, not the card's. A comment+move only happens on a delta.
func (w *Worker) reportPhase(ctx context.Context, card board.Card) error {
	rec, ok, err := w.store.GetCard(card.ID)
	if err != nil {
		return fmt.Errorf("read card record %s: %w", card.ID, err)
	}
	if !ok || rec.PRNumber == 0 {
		w.log.Warn("report skipped: no PR number for card", zap.String("card", card.ID))
		return nil
	}
	status, err := w.forge.PRStatus(ctx, card.Repo, rec.PRNumber)
	if err != nil {
		w.log.Warn("PRStatus read failed; skipping report (soft)", zap.String("card", card.ID), zap.Error(err))
		return nil
	}
	reviewChanged := status.ReviewDecision != rec.LastReviewState
	ciChanged := status.CIConclusion != rec.LastCIConclusion
	if !reviewChanged && !ciChanged {
		return nil
	}
	if comment := reportComment(status, reviewChanged, ciChanged); comment != "" {
		if err := w.board.PostComment(ctx, card.ID, comment); err != nil {
			return fmt.Errorf("post report comment: %w", err)
		}
	}
	if status.ReviewDecision == "changes_requested" || status.CIConclusion == "failure" {
		if err := w.board.MoveTo(ctx, card.ID, board.PhaseFailed); err != nil {
			return fmt.Errorf("move to Failed: %w", err)
		}
	}
	// Persist delta state only after a successful comment + move, so a mid-flight
	// failure re-reports on retry rather than being swallowed.
	rec.LastReviewState = status.ReviewDecision
	rec.LastCIConclusion = status.CIConclusion
	if err := w.store.PutCard(card.ID, rec); err != nil {
		return fmt.Errorf("persist report state: %w", err)
	}
	return nil
}

// reportComment renders one line per changed decision-grade dimension. Empty
// when nothing reportable changed (e.g. a transition to pending/review_required).
func reportComment(s forge.PRStatus, reviewChanged, ciChanged bool) string {
	var lines []string
	if reviewChanged {
		switch s.ReviewDecision {
		case "changes_requested":
			lines = append(lines, "🔄 Changes requested. Moving to Failed.")
		case "approved":
			lines = append(lines, "✅ PR approved.")
		}
	}
	if ciChanged {
		switch s.CIConclusion {
		case "failure":
			detail := ""
			if len(s.FailingChecks) > 0 {
				detail = ": " + strings.Join(s.FailingChecks, ", ")
			}
			lines = append(lines, "❌ CI failed"+detail+". Moving to Failed.")
		case "success":
			lines = append(lines, "✅ CI passed.")
		}
	}
	return strings.Join(lines, "\n")
}
```

(`forge` and `strings` are already imported in `worker.go`.)

- [ ] **Step 7: Run the orchestrator tests**

Run: `go test ./internal/orchestrator/ -v`
Expected: PASS — resolver table (incl. new rows), all report tests, existing worker tests.

- [ ] **Step 8: Full build, vet, and test sweep**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green, including `TestNoProviderImportsInOrchestrator` (the core still
imports only the ports + the new domain `forge.PRStatus`).

- [ ] **Step 9: Commit**

```bash
git add internal/orchestrator/decision.go internal/orchestrator/resolver.go internal/orchestrator/worker.go internal/orchestrator/resolver_test.go internal/orchestrator/report_test.go
git commit -m "feat(orchestrator): ActReport — observe PR review/CI and report (move to Failed on red)"
```

---

## Final verification (after all tasks)

- [ ] `go build ./...` — clean
- [ ] `go vet ./...` — clean
- [ ] `go test ./...` — all packages green (run with the `env -u WAZIR_*` prefix from the Conventions section if ambient config env interferes)
- [ ] `go test -race ./internal/orchestrator/ ./internal/store/` — clean (concurrent store + worker paths)
- [ ] `go build -tags integration ./...` — the integration test still compiles
- [ ] `go test ./internal/orchestrator/ -run TestNoProviderImportsInOrchestrator` — provider-free core intact

## Operational follow-up (NOT code — for the human operator)

Phase 1 does nothing in production until the GitHub App is reconfigured:

- [ ] In the App settings, **subscribe to** the `Pull request review` and `Check suite` webhook events.
- [ ] Grant the App **Checks: read** permission (org-admin re-consent / re-install).
- [ ] Restart `wazir serve` (this branch has no live reload) once merged + deployed.

These belong in the PR description / runbook, not in any task above.
