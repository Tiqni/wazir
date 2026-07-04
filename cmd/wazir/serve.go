package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/go-github/v66/github"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	boardgh "github.com/EmadMokhtar/wazir/internal/board/github"
	"github.com/EmadMokhtar/wazir/internal/claude"
	"github.com/EmadMokhtar/wazir/internal/config"
	forgegh "github.com/EmadMokhtar/wazir/internal/forge/github"
	"github.com/EmadMokhtar/wazir/internal/githubauth"
	"github.com/EmadMokhtar/wazir/internal/orchestrator"
	"github.com/EmadMokhtar/wazir/internal/queue"
	"github.com/EmadMokhtar/wazir/internal/server"
	"github.com/EmadMokhtar/wazir/internal/store"
)

func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the webhook receiver + orchestrator daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runServe(ctx, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "HTTP listen address for webhooks")
	return cmd
}

// restartOnlyChanged reports which restart-only config groups differ between two
// loads (auth/board/store/forge/claude.bin), so a reload can warn that they were
// ignored. Returns "" when nothing restart-only changed.
func restartOnlyChanged(oldCfg, newCfg config.Config) string {
	var changed []string
	og, ng := oldCfg.GitHub, newCfg.GitHub
	og.WebhookSecret, ng.WebhookSecret = "", "" // hot-reloaded; exclude from the compare
	if og != ng {
		changed = append(changed, "github")
	}
	if oldCfg.Project != newCfg.Project {
		changed = append(changed, "project")
	}
	if oldCfg.Store != newCfg.Store {
		changed = append(changed, "store")
	}
	if oldCfg.Forge != newCfg.Forge {
		changed = append(changed, "forge")
	}
	if oldCfg.Claude.Bin != newCfg.Claude.Bin {
		changed = append(changed, "claude.bin")
	}
	return strings.Join(changed, ", ")
}

// runServe wires the GitHub board + forge, the real claude brain, the queue,
// and the receiver, then serves until SIGINT/SIGTERM and drains.
func runServe(ctx context.Context, addr string) error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}
	auth, err := githubauth.New(ctx, cfg)
	if err != nil {
		return err
	}
	// Fail loud at startup if the App credentials can't mint an installation
	// token, rather than discovering it mid-turn (mirrors the plugin-dir resolve).
	if _, err := auth.GitToken(ctx); err != nil {
		return fmt.Errorf("mint installation token (check app_id/installation_id/private_key): %w", err)
	}
	st, err := store.OpenBbolt(cfg.Store.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	b := boardgh.New(auth.HTTPClient, cfg, st)
	// Load the board's cached identity (project node id + option→phase map) before
	// serving: ParseEvent drops projects_v2_item events whose project id != the
	// configured board, and that id is empty until hydrated — so without this every
	// column-move webhook is dropped and no card advances. Fail loudly if the board
	// hasn't been provisioned/bootstrapped yet.
	if err := b.Hydrate(ctx); err != nil {
		return fmt.Errorf("hydrate board (run `wazir provision` or `wazir bootstrap` first): %w", err)
	}
	f := forgegh.New(github.NewClient(auth.HTTPClient), forgegh.Options{
		GitBin:       cfg.Forge.GitBin,
		CloneRoot:    cfg.Forge.CloneRoot,
		WorktreeRoot: cfg.Forge.WorktreeRoot,
		Base:         cfg.Forge.BaseBranch,
		GitToken:     auth.GitToken,
	})
	// plan/execute seed each per-run config dir with a symlink to this plugin registry
	// + a settings.json enabling the configured plugin, so the Superpowers skills load
	// under isolation (M5 spike: --plugin-dir does not register them). Fail loudly at
	// startup if the registry is missing, not mid-turn.
	if fi, statErr := os.Stat(cfg.Claude.PluginsDir); statErr != nil || !fi.IsDir() {
		return fmt.Errorf("claude.plugins_dir %q is not a directory (set WAZIR_CLAUDE_PLUGINS_DIR): %v", cfg.Claude.PluginsDir, statErr)
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		logger.Warn("no claude auth env set (CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY); " +
			"headless runs will fail to authenticate under the isolated config dir")
	}
	brain := claude.New(cfg.Claude, logger)
	worker := orchestrator.NewWorker(b, f, brain, st, logger).
		WithMaxBrainstormTurns(cfg.Claude.MaxBrainstormTurns).
		WithBase(cfg.Forge.BaseBranch)

	// Live config reload: re-read wazir.yaml on change and hot-swap the safe
	// subset (claude.*, repos, bot_login, webhook_secret). Restart-only fields are
	// ignored with a warning. Disabled for an env-only run (no file to watch).
	if path, ok := config.ResolvePath(flagConfig); ok {
		// startCfg is the config the process actually applied at startup; it stays
		// fixed so restart-only fields are always compared against what's really
		// running, not against whatever the file last said. (Advancing it would
		// falsely warn when a restart-only field is reverted back to the running
		// value.) lastRestartDiff de-dups the warning so a standing divergence is
		// logged once, not on every reload. Both are touched only on the single
		// Watch goroutine — no data race.
		startCfg := cfg
		var lastRestartDiff string
		go func() {
			err := config.Watch(ctx, path,
				func(newCfg config.Config) {
					if d := restartOnlyChanged(startCfg, newCfg); d != lastRestartDiff {
						if d != "" {
							logger.Warn("config change requires a restart; ignored", zap.String("fields", d))
						}
						lastRestartDiff = d
					}
					brain.Reload(newCfg.Claude)
					b.Reload(newCfg.Repos, newCfg.BotLogin, newCfg.GitHub.WebhookSecret)
					worker.SetMaxBrainstormTurns(newCfg.Claude.MaxBrainstormTurns)
					logger.Info("config reloaded")
				},
				func(err error) { logger.Warn("config reload failed; keeping current config", zap.Error(err)) },
			)
			if err != nil {
				logger.Warn("live config reload disabled (watcher error)", zap.Error(err))
			}
		}()
	} else {
		logger.Info("live config reload disabled (no config file; env-only)")
	}

	// The queue runs on a context decoupled from the SIGINT signal so a graceful
	// drain lets in-flight claude turns finish (bounded by the per-turn timeout)
	// instead of cancelling them mid-flight. A single defer pins the order
	// (drain while queueCtx is live, then release it) so it can't be broken by a
	// future reorder, and still runs on every return path.
	queueCtx, cancelQueue := context.WithCancel(context.Background())

	q := queue.New(st, worker.Process, queue.Options{
		Workers: 4,
		Owner:   "wazir-serve",
		LockTTL: 5 * time.Minute,
		Logger:  logger,
	})
	q.Start(queueCtx)
	defer func() {
		q.Shutdown()  // drain in-flight turns while queueCtx is still live
		cancelQueue() // then release the queue context
	}()

	mux := http.NewServeMux()
	mux.Handle("/webhook", server.New(b, st, q, logger))
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Error("HTTP shutdown did not drain cleanly", zap.Error(err))
		}
	}()

	logger.Info("wazir serve listening", zap.String("addr", addr))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
