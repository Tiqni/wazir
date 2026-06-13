// Package githubauth produces GitHub App installation auth: an authenticated
// *http.Client for the REST + GraphQL clients and a token source for git.
// One ghinstallation.Transport mints and auto-refreshes the ~1h installation
// token and backs both.
package githubauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"

	"github.com/bradleyfalzon/ghinstallation/v2"

	"github.com/EmadMokhtar/wazir/internal/config"
)

// Auth carries the two auth surfaces the daemon needs, both backed by one
// ghinstallation.Transport so the installation token is minted/refreshed once.
type Auth struct {
	HTTPClient *http.Client                              // board REST+GraphQL AND forge REST (PRs)
	GitToken   func(ctx context.Context) (string, error) // a fresh installation token per git network op
}

// New builds the shared installation transport from the App config.
func New(ctx context.Context, cfg config.Config) (Auth, error) {
	keyBytes, err := loadPrivateKey(cfg.GitHub.PrivateKey)
	if err != nil {
		return Auth{}, err
	}
	tr, err := ghinstallation.New(http.DefaultTransport, cfg.GitHub.AppID, cfg.GitHub.InstallationID, keyBytes)
	if err != nil {
		return Auth{}, fmt.Errorf("parse app private key: %w", err)
	}
	return Auth{
		HTTPClient: &http.Client{Transport: tr},
		GitToken:   tr.Token, // (*ghinstallation.Transport).Token(ctx) (string, error)
	}, nil
}

// HTTPClient is a convenience for API-only callers (provision, card): it returns
// New(ctx, cfg).HTTPClient.
func HTTPClient(ctx context.Context, cfg config.Config) (*http.Client, error) {
	a, err := New(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return a.HTTPClient, nil
}

// loadPrivateKey resolves the configured private key to PEM bytes, auto-detecting
// the form: an existing file path is read; a base64-encoded PEM is decoded; any
// other value is treated as raw PEM bytes. ghinstallation.New validates parseability.
func loadPrivateKey(v string) ([]byte, error) {
	if v == "" {
		return nil, fmt.Errorf("github.private_key is empty (set WAZIR_GITHUB_PRIVATE_KEY)")
	}
	if fi, err := os.Stat(v); err == nil && !fi.IsDir() {
		return os.ReadFile(v)
	}
	if decoded, err := base64.StdEncoding.DecodeString(v); err == nil && bytes.Contains(decoded, []byte("PRIVATE KEY")) {
		return decoded, nil
	}
	return []byte(v), nil
}
