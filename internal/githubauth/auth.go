// Package githubauth produces an authenticated HTTP client for the GitHub
// REST and GraphQL clients. PAT ships now; GitHub App is scaffolded.
package githubauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// ErrAppAuthNotWired is returned for GITHUB_AUTH=app in M0 (delivered later).
var ErrAppAuthNotWired = errors.New("githubauth: GitHub App auth not wired in M0")

// HTTPClient returns an authenticated *http.Client for the configured mode.
// Board and forge implementations receive this and never see auth details.
func HTTPClient(ctx context.Context, cfg config.Config) (*http.Client, error) {
	switch cfg.GitHubAuth {
	case "pat":
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.GitHubPAT})
		return oauth2.NewClient(ctx, ts), nil
	case "app":
		// Scaffold: ghinstallation.NewKeyFromFile / NewAppsTransport wiring
		// lands when webhooks (M1+) require an App. Compiles, fails loudly.
		return nil, ErrAppAuthNotWired
	default:
		return nil, fmt.Errorf("githubauth: unknown GITHUB_AUTH %q", cfg.GitHubAuth)
	}
}
