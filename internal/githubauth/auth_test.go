package githubauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/config"
)

func TestPATClientSetsBearerHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := HTTPClient(context.Background(), config.Config{
		GitHub: config.GitHubConfig{Auth: "pat", PAT: "tok123"},
	})
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
	}
}

func TestAppNotWiredYet(t *testing.T) {
	_, err := HTTPClient(context.Background(), config.Config{
		GitHub: config.GitHubConfig{Auth: "app", AppID: 1, PrivateKey: "x"},
	})
	if !errors.Is(err, ErrAppAuthNotWired) {
		t.Fatalf("want ErrAppAuthNotWired, got %v", err)
	}
}
