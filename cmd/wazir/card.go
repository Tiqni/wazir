package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func newCardCmd() *cobra.Command {
	card := &cobra.Command{
		Use:   "card",
		Short: "Operate on a card directly (dev/manual)",
	}
	card.AddCommand(
		&cobra.Command{
			Use:   "move <issue-node-id> <phase>",
			Short: "Move a card to a phase column",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runCardMove(cmd.Context(), args[0], args[1])
			},
		},
		&cobra.Command{
			Use:   "comment <issue-node-id> <text>",
			Short: "Post a comment on a card",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return runCardComment(cmd.Context(), args[0], args[1])
			},
		},
	)
	return card
}

func runCardMove(ctx context.Context, cardID, phaseStr string) error {
	phase := board.Phase(phaseStr)
	if !phase.Valid() {
		return fmt.Errorf("invalid phase %q", phaseStr)
	}
	b, st, _, err := openBoard(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := b.MoveTo(ctx, cardID, phase); err != nil {
		return err
	}
	logger.Info("moved card", zap.String("card", cardID), zap.String("phase", phaseStr))
	fmt.Printf("moved %s to %s\n", cardID, phase)
	return nil
}

func runCardComment(ctx context.Context, cardID, text string) error {
	b, st, _, err := openBoard(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := b.PostComment(ctx, cardID, text); err != nil {
		return err
	}
	logger.Info("commented on card", zap.String("card", cardID))
	fmt.Printf("commented on %s\n", cardID)
	return nil
}
