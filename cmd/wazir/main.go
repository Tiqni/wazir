package main

import (
	"context"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "wazir:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wazir <provision|bootstrap|card> [args]")
	}
	ctx := context.Background()
	switch args[0] {
	case "provision":
		return cmdProvision(ctx, true)
	case "bootstrap":
		return cmdProvision(ctx, false)
	case "card":
		return cmdCard(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
