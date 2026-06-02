package githubauth

import (
	"context"
	"errors"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/config"
)

func TestPATReturnsClient(t *testing.T) {
	c, err := HTTPClient(context.Background(), config.Config{GitHubAuth: "pat", GitHubPAT: "tok"})
	if err != nil {
		t.Fatalf("HTTPClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestAppNotWiredYet(t *testing.T) {
	_, err := HTTPClient(context.Background(), config.Config{GitHubAuth: "app", GitHubAppID: 1, GitHubPrivateKey: "x"})
	if !errors.Is(err, ErrAppAuthNotWired) {
		t.Fatalf("want ErrAppAuthNotWired, got %v", err)
	}
}
