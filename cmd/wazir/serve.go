package main

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
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

// runServe wires the GitHub board + forge, the real claude brain, the queue,
// and the receiver, then serves until SIGINT/SIGTERM and drains.
func runServe(ctx context.Context, addr string) error {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return err
	}
	hc, err := githubauth.HTTPClient(ctx, cfg)
	if err != nil {
		return err
	}
	st, err := store.OpenBbolt(cfg.Store.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	b := boardgh.New(hc, cfg, st)
	f := forgegh.New(github.NewClient(hc))
	brain := claude.New(cfg.Claude, logger)
	worker := orchestrator.NewWorker(b, f, brain, st, logger).
		WithMaxBrainstormTurns(cfg.Claude.MaxBrainstormTurns)

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
