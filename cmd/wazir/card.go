package main

import (
	"context"
	"fmt"

	"github.com/EmadMokhtar/wazir/internal/board"
)

func cmdCard(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: wazir card <move|comment> <issue-node-id> [args]")
	}
	sub, cardID := args[0], args[1]
	b, st, _, err := openBoard(ctx)
	if err != nil {
		return err
	}
	defer st.Close()

	switch sub {
	case "move":
		if len(args) < 3 {
			return fmt.Errorf("usage: wazir card move <issue-node-id> <phase>")
		}
		phase := board.Phase(args[2])
		if !phase.Valid() {
			return fmt.Errorf("invalid phase %q", args[2])
		}
		if err := b.MoveTo(ctx, cardID, phase); err != nil {
			return err
		}
		fmt.Printf("moved %s to %s\n", cardID, phase)
		return nil
	case "comment":
		if len(args) < 3 {
			return fmt.Errorf("usage: wazir card comment <issue-node-id> <text>")
		}
		if err := b.PostComment(ctx, cardID, args[2]); err != nil {
			return err
		}
		fmt.Printf("commented on %s\n", cardID)
		return nil
	default:
		return fmt.Errorf("unknown card subcommand %q", sub)
	}
}
