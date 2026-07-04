package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/EmadMokhtar/wazir/internal/board"
	"github.com/EmadMokhtar/wazir/internal/config"
	"github.com/EmadMokhtar/wazir/internal/store"
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
		&cobra.Command{
			Use:   "link-pr <issue-node-id> <pr-number>",
			Short: "Backfill a card's PR linkage (number + PR-index) for a PR opened before PR-watch existed",
			Long: "Writes CardRecord.PRNumber and the repo#pr -> issue PR-index so PR review / " +
				"check-suite webhooks (and the @wazir fix command) resolve to the card. Use for PRs " +
				"Wazir opened before it persisted the linkage automatically. Stop `wazir serve` first " +
				"(it holds the store lock); the repo is read from the card's existing record.",
			Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				n, err := strconv.Atoi(args[1])
				if err != nil {
					return fmt.Errorf("pr-number must be an integer: %w", err)
				}
				return runCardLinkPR(args[0], n)
			},
		},
	)
	return card
}

func runCardLinkPR(cardID string, prNumber int) error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}
	st, err := store.OpenBbolt(cfg.Store.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	repo, err := linkPRInStore(st, cardID, prNumber)
	if err != nil {
		return err
	}
	logger.Info("linked PR to card", zap.String("card", cardID), zap.String("repo", repo), zap.Int("pr", prNumber))
	fmt.Printf("linked PR %s#%d -> card %s\n", repo, prNumber, cardID)
	return nil
}

// linkPRInStore backfills a card's PR linkage: it sets CardRecord.PRNumber and writes
// the repo#pr -> issue PR-index. The repo comes from the card's existing record, so the
// card must already have been resolved by Wazir. Returns the repo it linked.
func linkPRInStore(st store.Store, cardID string, prNumber int) (string, error) {
	rec, ok, err := st.GetCard(cardID)
	if err != nil {
		return "", err
	}
	if !ok || rec.Repo == "" {
		return "", fmt.Errorf("no stored card record with a repo for %s; the card must have been resolved by wazir first", cardID)
	}
	rec.PRNumber = prNumber
	if err := st.PutCard(cardID, rec); err != nil {
		return "", err
	}
	if err := st.PutPRIndex(rec.Repo, prNumber, cardID); err != nil {
		return "", err
	}
	return rec.Repo, nil
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
