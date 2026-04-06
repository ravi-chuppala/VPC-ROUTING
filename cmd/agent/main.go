package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ravi-chuppala/vpc-routing/internal/agent"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Initialize netlink manager (in-memory for now; swap for real netlink in production)
	nl := agent.NewInMemoryNetlink()

	// Start reconciler
	reconciler := agent.NewReconciler(nl, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reconciler.Start(ctx)

	slog.Info("vpc-interconnect-agent started", "reconcile_interval", "30s")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down agent")
	reconciler.Stop()
}
