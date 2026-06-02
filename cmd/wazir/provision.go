package main

import (
	"context"
	"fmt"

	"github.com/EmadMokhtar/wazir/internal/board"
	boardgh "github.com/EmadMokhtar/wazir/internal/board/github"
	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func openBoard(ctx context.Context) (board.Board, store.Store, config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	hc, err := githubauth.HTTPClient(ctx, cfg)
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	st, err := store.OpenBbolt(cfg.DBPath)
	if err != nil {
		return nil, nil, config.Config{}, err
	}
	return boardgh.New(hc, cfg, st), st, cfg, nil
}

func cmdProvision(ctx context.Context, create bool) error {
	b, st, cfg, err := openBoard(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	spec := board.BoardSpec{Name: cfg.BoardName, Columns: board.AllPhases(), Create: create}
	if err := b.EnsureProvisioned(ctx, spec); err != nil {
		return err
	}
	action := "provisioned"
	if !create {
		action = "bootstrapped"
	}
	fmt.Printf("board %q %s for %s (project #%d)\n", cfg.BoardName, action, cfg.ProjectOwner, cfg.ProjectNumber)
	return nil
}
