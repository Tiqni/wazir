package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/retry"
)

func TestOpenPRPostsCorrectRequest(t *testing.T) {
	var gotPath string
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number":9,"html_url":"https://github.com/octocat/hello/pull/9"}`))
	}))
	defer srv.Close()

	c := github.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	c.BaseURL = u
	f := &GitHubForge{rest: c}

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
	if gotPath != "/repos/octocat/hello/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if payload["head"] != "feature/x" || payload["base"] != "main" || payload["title"] != "Add X" {
		t.Errorf("payload = %+v", payload)
	}
}

func TestNewDefaultsGitRetryPolicy(t *testing.T) {
	f := New(nil, Options{}) // zero RetryPolicy
	if f.git.policy.MaxAttempts < 2 {
		t.Fatalf("New must default a zero RetryPolicy to a retrying one; got MaxAttempts=%d", f.git.policy.MaxAttempts)
	}
}

func TestNewHonorsExplicitGitRetryPolicy(t *testing.T) {
	want := retry.Policy{MaxAttempts: 7, BaseDelay: time.Second, MaxDelay: 2 * time.Second}
	f := New(nil, Options{RetryPolicy: want})
	if f.git.policy != want {
		t.Fatalf("New must keep an explicit RetryPolicy; got %+v", f.git.policy)
	}
}

func TestNewFillsMissingDelayFields(t *testing.T) {
	// A caller sets MaxAttempts but leaves the delays zero: New must fill them so
	// retry.Backoff doesn't return 0 (a tight, no-backoff loop).
	f := New(nil, Options{RetryPolicy: retry.Policy{MaxAttempts: 3}})
	if f.git.policy.MaxAttempts != 3 {
		t.Fatalf("MaxAttempts must be preserved; got %d", f.git.policy.MaxAttempts)
	}
	if f.git.policy.BaseDelay <= 0 || f.git.policy.MaxDelay <= 0 {
		t.Fatalf("New must fill missing delay fields; got BaseDelay=%v MaxDelay=%v",
			f.git.policy.BaseDelay, f.git.policy.MaxDelay)
	}
}
