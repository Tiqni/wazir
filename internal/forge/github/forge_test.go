package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v66/github"
)

func TestOpenPRPostsCorrectRequest(t *testing.T) {
	var gotPath string
	var payload map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &payload)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"html_url":"https://github.com/octocat/hello/pull/9"}`))
	}))
	defer srv.Close()

	c := github.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	c.BaseURL = u
	f := &GitHubForge{rest: c}

	prURL, err := f.OpenPR(context.Background(), "octocat/hello", "feature/x", "main", "Add X", "body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if prURL != "https://github.com/octocat/hello/pull/9" {
		t.Errorf("prURL = %q", prURL)
	}
	if gotPath != "/repos/octocat/hello/pulls" {
		t.Errorf("path = %q", gotPath)
	}
	if payload["head"] != "feature/x" || payload["base"] != "main" || payload["title"] != "Add X" {
		t.Errorf("payload = %+v", payload)
	}
}
