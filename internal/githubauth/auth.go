// Package githubauth produces an authenticated HTTP client for the GitHub
// REST and GraphQL clients. PAT ships now; GitHub App is scaffolded.
package githubauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// ErrAppAuthNotWired is returned for github.auth=app in M0 (delivered later).
var ErrAppAuthNotWired = errors.New("githubauth: GitHub App auth not wired in M0")

// bearerTransport adds a static "Authorization: Bearer <token>" header to every
// request. GitHub accepts this for both REST and GraphQL with a PAT. It wraps a
// base RoundTripper (http.DefaultTransport when nil). This is all we needed from
// golang.org/x/oauth2's StaticTokenSource, without the dependency.
type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The RoundTripper contract forbids mutating the caller's request, so clone.
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// HTTPClient returns an authenticated *http.Client for the configured mode.
// Board and forge implementations receive this and never see auth details.
func HTTPClient(ctx context.Context, cfg config.Config) (*http.Client, error) {
	switch cfg.GitHub.Auth {
	case "pat":
		return &http.Client{Transport: &bearerTransport{token: cfg.GitHub.PAT}}, nil
	case "app":
		// Scaffold: ghinstallation provides its own RoundTripper; wiring lands
		// when webhooks (M1+) require an App. Compiles, fails loudly.
		return nil, ErrAppAuthNotWired
	default:
		return nil, fmt.Errorf("githubauth: unknown github.auth %q", cfg.GitHub.Auth)
	}
}
