package main

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// newLogger builds a zap logger for the given level and format.
// format is "console" (human-readable, default) or "json" (structured).
func newLogger(level, format string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		return nil, fmt.Errorf("invalid --log-level %q: %w", level, err)
	}
	var cfg zap.Config
	switch format {
	case "json":
		cfg = zap.NewProductionConfig()
	case "console", "":
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	default:
		return nil, fmt.Errorf("invalid --log-format %q (want console|json)", format)
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	cfg.DisableStacktrace = true // CLI errors are messages, not crash traces
	return cfg.Build()
}
