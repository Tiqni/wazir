package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"
)

// TestCreateWorktreeFromBranchUsesRemoteHead asserts the worktree is created at
// origin/<branch> (the PR head), NOT origin/<base> — so the PR's commits survive.
func TestCreateWorktreeFromBranchUsesRemoteHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	origin := seedBareOrigin(t) // main has one commit "seed"
	f := newTestForge(t, origin)
	const repo = "owner/name"
	const branch = "feature/issue-7-x"

	if _, err := f.EnsureClone(ctx, repo); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	// Simulate a PR branch on origin ahead of main: build it via the normal (reset-to-base)
	// worktree, add a commit, push it.
	wt, err := f.CreateWorktree(ctx, repo, branch)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@w", "-c", "user.name=W"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(wt, "commit", "--allow-empty", "-m", "pr work")
	if err := f.PushBranch(ctx, repo, branch); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if err := f.RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}

	// Now recreate from the remote PR head — the "pr work" commit must be present.
	wt2, err := f.CreateWorktreeFromBranch(ctx, repo, branch)
	if err != nil {
		t.Fatalf("CreateWorktreeFromBranch: %v", err)
	}
	if wt2 == "" {
		t.Fatal("empty worktree path")
	}
	logCmd := exec.Command("git", "log", "--oneline")
	logCmd.Dir = wt2
	out, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "pr work") {
		t.Errorf("worktree missing the PR commit (was it reset to base?):\n%s", out)
	}
}

// prFeedbackServer stubs reviews + inline comments.
func prFeedbackServer(t *testing.T, reviewsBody, commentsBody string) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			w.Write([]byte(reviewsBody))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			w.Write([]byte(commentsBody))
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

func TestPRReviewFeedbackCollectsBodyAndInline(t *testing.T) {
	reviews := `[
		{"user":{"login":"alice"},"state":"CHANGES_REQUESTED","body":"please fix the error handling","submitted_at":"2026-06-19T10:00:00Z"}
	]`
	comments := `[
		{"user":{"login":"alice"},"path":"main.go","line":42,"body":"wrap this error"}
	]`
	f := &GitHubForge{rest: prFeedbackServer(t, reviews, comments)}

	fb, err := f.PRReviewFeedback(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRReviewFeedback: %v", err)
	}
	if fb.Summary != "please fix the error handling" {
		t.Errorf("Summary = %q", fb.Summary)
	}
	if len(fb.Comments) != 1 || fb.Comments[0].Path != "main.go" || fb.Comments[0].Line != 42 || fb.Comments[0].Body != "wrap this error" {
		t.Errorf("Comments = %+v", fb.Comments)
	}
}

func TestPRReviewFeedbackEmptyIsValid(t *testing.T) {
	f := &GitHubForge{rest: prFeedbackServer(t, `[]`, `[]`)}
	fb, err := f.PRReviewFeedback(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("PRReviewFeedback: %v", err)
	}
	if fb.Summary != "" || len(fb.Comments) != 0 {
		t.Errorf("expected empty feedback, got %+v", fb)
	}
}

// checkAnnServer stubs the PR head lookup, its check-runs, and per-run annotations.
func checkAnnServer(t *testing.T, prBody, checkRunsBody, annotationsBody string) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/check-runs/") && strings.HasSuffix(r.URL.Path, "/annotations"):
			w.Write([]byte(annotationsBody))
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

func TestCheckAnnotationsCollectsFailedRuns(t *testing.T) {
	pr := `{"number":9,"head":{"sha":"abc123"}}`
	runs := `{"total_count":2,"check_runs":[
		{"id":111,"name":"lint","status":"completed","conclusion":"failure"},
		{"id":222,"name":"unit","status":"completed","conclusion":"success"}
	]}`
	// Only run 111 (failure) should be queried for annotations.
	anns := `[{"path":"main.go","start_line":10,"annotation_level":"failure","message":"undefined: foo"}]`
	f := &GitHubForge{rest: checkAnnServer(t, pr, runs, anns)}

	got, err := f.CheckAnnotations(context.Background(), "octocat/hello", 9)
	if err != nil {
		t.Fatalf("CheckAnnotations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("annotations = %+v, want 1", got)
	}
	a := got[0]
	if a.Check != "lint" || a.Path != "main.go" || a.Line != 10 || a.Level != "failure" || a.Message != "undefined: foo" {
		t.Errorf("annotation = %+v", a)
	}
}
