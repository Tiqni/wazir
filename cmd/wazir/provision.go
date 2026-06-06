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

// pruneFlags adds the shared --prune/--force flags to a reconcile command.
func pruneFlags(cmd *cobra.Command, prune, force *bool) {
	cmd.Flags().BoolVar(prune, "prune", false, "reconcile to EXACTLY Wazir's columns, deleting any others (destructive)")
	cmd.Flags().BoolVar(force, "force", false, "with --prune, delete a column even if it still holds cards")
}

func newProvisionCmd() *cobra.Command {
	var prune, force bool
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Create the board if absent and reconcile its phase columns",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvision(cmd.Context(), true, prune, force)
		},
	}
	pruneFlags(cmd, &prune, &force)
	return cmd
}

func newBootstrapCmd() *cobra.Command {
	var prune, force bool
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Reconcile and cache an existing board's columns (no create)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvision(cmd.Context(), false, prune, force)
		},
	}
	pruneFlags(cmd, &prune, &force)
	return cmd
}

func runProvision(ctx context.Context, create, prune, force bool) error {
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
		zap.Bool("prune", prune),
		zap.Bool("force", force),
	)

	spec := board.BoardSpec{Name: cfg.Project.BoardName, Columns: board.AllPhases(), Create: create, Prune: prune, Force: force}
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
