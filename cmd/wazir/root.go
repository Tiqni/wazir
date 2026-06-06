package main

import (
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// Persistent flags + the process logger, shared across subcommands.
var (
	flagConfig    string
	flagLogLevel  string
	flagLogFormat string

	logger *zap.Logger
)

// newRootCmd builds the `wazir` command tree.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "wazir",
		Short:         "Wazir — a board-driven, human-gated AI dev loop orchestrator",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			l, err := newLogger(flagLogLevel, flagLogFormat)
			if err != nil {
				return err
			}
			logger = l
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagConfig, "config", "", "path to config file (default: ./wazir.yaml, else env only)")
	pf.StringVar(&flagLogLevel, "log-level", "info", "log level (debug|info|warn|error)")
	pf.StringVar(&flagLogFormat, "log-format", "console", "log format (console|json)")

	root.AddCommand(newProvisionCmd(), newBootstrapCmd(), newCardCmd())
	return root
}
