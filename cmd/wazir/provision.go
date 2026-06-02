package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	boardgh "github.com/EmadMokhtar/wazir/internal/board/github"
	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
	"github.com/EmadMokhtar/wazir/internal/store"
)

// openBoard loads config, builds an authenticated client + store, and returns
// the board through the port interface (provider type stays invisible here).
func openBoard(ctx context.Context) (board.Board, store.Store, config.Config, error) {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	hc, err := githubauth.HTTPClient(ctx, cfg)
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	st, err := store.OpenBbolt(cfg.Store.DBPath)
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	return boardgh.New(hc, cfg, st), st, cfg, nil
}

func newProvisionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "provision",
		Short: "Create the board if absent and reconcile its phase columns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvision(cmd.Context(), true)
		},
	}
}

func newBootstrapCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "bootstrap",
		Short: "Reconcile and cache an existing board's columns (no create)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvision(cmd.Context(), false)
		},
	}
}

func runProvision(ctx context.Context, create bool) error {
	b, st, cfg, err := openBoard(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	action := "bootstrap"
	if create {
		action = "provision"
	}
	logger.Info("reconciling board",
		zap.String("action", action),
		zap.String("owner", cfg.Project.Owner),
		zap.Int("project", cfg.Project.Number),
	)

	spec := board.BoardSpec{Name: cfg.Project.BoardName, Columns: board.AllPhases(), Create: create}
	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		return err
	}

	logger.Info("board ready",
		zap.String("board", cfg.Project.BoardName),
		zap.Int("project", cfg.Project.Number),
	)
	fmt.Printf("board %q ready for %s (project #%d)\n", cfg.Project.BoardName, cfg.Project.Owner, cfg.Project.Number)
	return nil
}
