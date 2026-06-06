package main

import (
	"fmt"
	"os"

	"go.uber.org/zap"
)

func main() {
	err := newRootCmd().Execute()
	if logger != nil {
		if err != nil {
			logger.Error("command failed", zap.Error(err))
		}
		_ = logger.Sync()
	} else if err != nil {
		// Failure before the logger was built (flag parse / pre-run).
		fmt.Fprintln(os.Stderr, "wazir:", err)
	}
	if err != nil {
		os.Exit(1)
	}
}
