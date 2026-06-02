package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func restClient(t *testing.T, h http.HandlerFunc) *github.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := github.NewClient(nil)
	u, _ := url.Parse(srv.URL + "/")
	c.BaseURL = u
	return c
}

func TestPostCommentStampsMarkerAndUsesCardRepo(t *testing.T) {
	var gotPath, gotBody string
	rest := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]string
		json.Unmarshal(raw, &payload)
		gotBody = payload["body"]
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":1}`))
	})
	st := store.NewMemory()
	st.PutCard("ISSUE1", store.CardRecord{Repo: "octocat/hello", IssueNumber: 42})
	b := &GitHubBoard{rest: rest, store: st}

	if err := b.PostComment(context.Background(), "ISSUE1", "ping"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotPath != "/repos/octocat/hello/issues/42/comments" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotBody, "ping") || !strings.Contains(gotBody, botMarker) {
		t.Errorf("body = %q, want ping + marker", gotBody)
	}
}

func TestSetBodyKeepsOriginalInDetails(t *testing.T) {
	var gotBody string
	rest := restClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"number":42,"body":"original idea"}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(raw, &payload)
		gotBody, _ = payload["body"].(string)
		w.Write([]byte(`{"number":42}`))
	})
	st := store.NewMemory()
	st.PutCard("ISSUE1", store.CardRecord{Repo: "octocat/hello", IssueNumber: 42})
	b := &GitHubBoard{rest: rest, store: st}

	if err := b.SetBody(context.Background(), "ISSUE1", "# Spec\nnew"); err != nil {
		t.Fatalf("SetBody: %v", err)
	}
	if !strings.Contains(gotBody, "# Spec") || !strings.Contains(gotBody, "<details>") || !strings.Contains(gotBody, "original idea") {
		t.Errorf("body = %q, want spec + collapsed original", gotBody)
	}
	if strings.Index(gotBody, "<details>") > strings.Index(gotBody, "# Spec") {
		t.Errorf("collapsed original idea should be at the top, before the spec; got %q", gotBody)
	}
}

func TestResolveCardUsesNodeFallbackThenCaches(t *testing.T) {
	api := &fakeAPI{}
	api.resolveRepo = "octocat/world"
	api.resolveNumber = 7
	st := store.NewMemory()
	b := &GitHubBoard{api: api, store: st}

	ref, err := b.resolveCard(context.Background(), "ISSUE2")
	if err != nil {
		t.Fatalf("resolveCard: %v", err)
	}
	if ref.Repo != "octocat/world" || ref.Number != 7 {
		t.Errorf("ref = %+v", ref)
	}
	if !api.resolveCalled {
		t.Error("expected node fallback on cold cache")
	}
	// Second call hits the cache, not the API.
	api.resolveCalled = false
	if _, err := b.resolveCard(context.Background(), "ISSUE2"); err != nil {
		t.Fatal(err)
	}
	if api.resolveCalled {
		t.Error("expected cache hit on second resolve")
	}
}

func TestMoveToTranslatesPhaseToOptionID(t *testing.T) {
	api := &fakeAPI{findItemID: "ITEM1", findItemFound: true}
	st := store.NewMemory()
	b := &GitHubBoard{api: api, store: st}
	b.cached = &store.BoardRecord{
		ProjectNodeID: "P1", StatusFieldID: "F1",
		Options: map[string]string{string(board.PhaseBrainstorming): "opt-brain"},
	}
	if err := b.MoveTo(context.Background(), "ISSUE1", board.PhaseBrainstorming); err != nil {
		t.Fatalf("MoveTo: %v", err)
	}
	if api.setOptionID != "opt-brain" || api.setItemID != "ITEM1" {
		t.Errorf("SetItemStatus got item=%q option=%q", api.setItemID, api.setOptionID)
	}
}
