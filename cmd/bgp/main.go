package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	config := bgp.Config{
		LocalASN:        65000,
		RouterID:        "10.0.0.1",
		ListenPort:      179,
		RouteReflectors: []string{"10.0.0.100", "10.0.0.101"},
	}

	svc := bgp.NewInMemoryService(config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := svc.Start(ctx); err != nil {
		slog.Error("failed to start BGP service", "error", err)
		os.Exit(1)
	}

	slog.Info("vpc-interconnect-bgp started",
		"asn", config.LocalASN,
		"router_id", config.RouterID,
		"peers", len(config.RouteReflectors),
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down BGP service")
	svc.Stop()
}
