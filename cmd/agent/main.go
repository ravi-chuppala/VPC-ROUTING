package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ravi-chuppala/vpc-routing/internal/agent"
	"github.com/ravi-chuppala/vpc-routing/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadAgentConfig()

	nl := agent.NewInMemoryNetlink()
	reconciler := agent.NewReconciler(nl, cfg.ReconcileInterval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reconciler.Start(ctx)

	slog.Info("vpc-interconnect-agent started",
		"reconcile_interval", cfg.ReconcileInterval,
		"controller_addr", cfg.ControllerAddr,
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down agent")
	reconciler.Stop()
}
