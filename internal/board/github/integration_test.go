//go:build integration

package github

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// Run with:
//
//	GITHUB_AUTH=pat GITHUB_PAT=... OWNER_TYPE=user PROJECT_OWNER=you \
//	PROJECT_NUMBER=NN WAZIR_DB=$(mktemp) \
//	go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
func TestIntegrationProvision(t *testing.T) {
	if os.Getenv("PROJECT_NUMBER") == "" {
		t.Skip("set PROJECT_NUMBER to run the integration test")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	hc, err := githubauth.HTTPClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	st := store.NewMemory()
	b := New(hc, cfg, st)
	ctx := context.Background()
	spec := board.BoardSpec{Name: cfg.BoardName, Columns: board.AllPhases(), Create: true}

	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	// Idempotency: a second run must converge with no error and no duplicates.
	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		t.Fatalf("second provision: %v", err)
	}

	num, _ := strconv.Atoi(os.Getenv("PROJECT_NUMBER"))
	_ = num
	// Assert every phase column was cached.
	for _, p := range board.AllPhases() {
		if b.cached.Options[string(p)] == "" {
			t.Errorf("phase %s has no option id after provision", p)
		}
	}
}
