//go:build integration

package github

import (
	"context"
	"os"
	"testing"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// Run with (env-only config, no file needed). The App must be installed on the org:
//
//	WAZIR_GITHUB_APP_ID=... WAZIR_GITHUB_INSTALLATION_ID=... WAZIR_GITHUB_PRIVATE_KEY=/path/key.pem \
//	WAZIR_GITHUB_OWNER_TYPE=org WAZIR_PROJECT_OWNER=your-org WAZIR_PROJECT_NUMBER=NN \
//	go test -tags integration ./internal/board/github/ -run TestIntegrationProvision -v
func TestIntegrationProvision(t *testing.T) {
	if os.Getenv("WAZIR_PROJECT_NUMBER") == "" {
		t.Skip("set WAZIR_PROJECT_NUMBER to run the integration test")
	}
	cfg, err := config.Load("")
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
	spec := board.BoardSpec{Name: cfg.Project.BoardName, Columns: board.AllPhases(), Create: true}

	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	// Idempotency: a second run must converge with no error and no duplicates.
	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		t.Fatalf("second provision: %v", err)
	}

	// Assert every phase column was cached.
	for _, p := range board.AllPhases() {
		if b.cached.Options[string(p)] == "" {
			t.Errorf("phase %s has no option id after provision", p)
		}
	}

	// ItemStatus / Phase resolution note (M2):
	// The ItemStatus seam and phaseFromOption helper are covered by unit tests
	// (TestGetCardPopulatesPhaseFromStatus, TestGetCardEmptyPhaseWhenItemNotOnBoard)
	// with the fakeAPI in board_test.go. Live coverage of ItemStatus is exercised
	// via GetCard once a card is on the board — no extra live assertion is added
	// here because seeding a card with a known Status requires additional API calls
	// that duplicate the unit coverage without adding signal.
}
