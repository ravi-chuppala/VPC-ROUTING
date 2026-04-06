package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/controller"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Initialize stores (in-memory for now; swap for PostgreSQL in production)
	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	// Initialize BGP service
	bgpSvc := bgp.NewInMemoryService(bgp.Config{
		LocalASN:        65000,
		RouterID:        "10.0.0.1",
		RouteReflectors: []string{"10.0.0.100"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bgpSvc.Start(ctx)

	// Start reconciler
	reconciler := controller.NewReconciler(vpcs, peerings, events, routes, bgpSvc, 10*time.Second)
	reconciler.Start(ctx)

	slog.Info("vpc-interconnect-controller started", "reconcile_interval", "10s")

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down controller")
	reconciler.Stop()
	bgpSvc.Stop()
}
