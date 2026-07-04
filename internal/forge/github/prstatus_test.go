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

func TestPRStatusTwoReviewersChangesRequestedWins(t *testing.T) {
	// Distinct reviewers: one approves, one requests changes => changes_requested.
	reviews := `[
		{"user":{"login":"alice"},"state":"APPROVED","submitted_at":"2026-06-17T10:00:00Z"},
		{"user":{"login":"bob"},"state":"CHANGES_REQUESTED","submitted_at":"2026-06-17T10:30:00Z"}
	]`
	checks := `{"total_count":0,"check_runs":[]}`
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, reviews, checks)}

	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.ReviewDecision != "changes_requested" {
		t.Errorf("ReviewDecision = %q, want changes_requested (one reviewer blocks)", st.ReviewDecision)
	}
}

func TestPRStatusEmptyRunsSliceIsEmpty(t *testing.T) {
	// total_count disagrees with an empty check_runs slice (pagination edge):
	// guard on the slice, never report a false "success".
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, `[]`, `{"total_count":5,"check_runs":[]}`)}
	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.CIConclusion != "" {
		t.Errorf("CIConclusion = %q, want empty (no runs in the slice)", st.CIConclusion)
	}
}

func TestPRStatusPendingWinsOverFailure(t *testing.T) {
	// A still-running run alongside a failed one => pending wins (suite not settled).
	reviews := `[]`
	checks := `{"total_count":2,"check_runs":[
		{"name":"lint","status":"completed","conclusion":"failure"},
		{"name":"unit","status":"in_progress","conclusion":""}
	]}`
	f := &GitHubForge{rest: prStatusServer(t, prHeadSHA, reviews, checks)}

	st, err := f.PRStatus(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRStatus: %v", err)
	}
	if st.CIConclusion != "pending" {
		t.Errorf("CIConclusion = %q, want pending (a run is still in progress)", st.CIConclusion)
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
