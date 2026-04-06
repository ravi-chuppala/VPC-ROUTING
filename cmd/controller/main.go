package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/config"
	"github.com/ravi-chuppala/vpc-routing/internal/controller"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadControllerConfig()

	// Initialize stores (in-memory for dev; swap for PostgreSQL via DATABASE_URL)
	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	bgpSvc := bgp.NewInMemoryService(bgp.Config{
		LocalASN:        cfg.BGP.LocalASN,
		RouterID:        cfg.BGP.RouterID,
		RouteReflectors: cfg.BGP.RouteReflectors,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bgpSvc.Start(ctx)

	reconciler := controller.NewReconciler(vpcs, peerings, events, routes, bgpSvc, cfg.ReconcileInterval)
	reconciler.Start(ctx)

	slog.Info("vpc-interconnect-controller started",
		"reconcile_interval", cfg.ReconcileInterval,
		"bgp_asn", cfg.BGP.LocalASN,
	)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	slog.Info("shutting down controller")
	reconciler.Stop()
	bgpSvc.Stop()
}
