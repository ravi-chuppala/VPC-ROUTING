package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadBGPConfig()

	svc := bgp.NewInMemoryService(bgp.Config{
		LocalASN:        cfg.LocalASN,
		RouterID:        cfg.RouterID,
		ListenPort:      cfg.ListenPort,
		RouteReflectors: cfg.RouteReflectors,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Start(ctx); err != nil {
		slog.Error("failed to start BGP service", "error", err)
		os.Exit(1)
	}

	slog.Info("vpc-interconnect-bgp started",
		"asn", cfg.LocalASN,
		"router_id", cfg.RouterID,
		"peers", len(cfg.RouteReflectors),
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down BGP service")
	svc.Stop()
}
