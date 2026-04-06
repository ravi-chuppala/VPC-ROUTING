package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ravi-chuppala/vpc-routing/internal/api"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/config"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadAPIConfig()

	// Initialize stores (in-memory for dev; swap for PostgreSQL via DATABASE_URL)
	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	alloc := vni.NewAllocator()
	for _, r := range cfg.Regions {
		alloc.RegisterRegion(r.Name, r.ID)
	}

	router := api.NewRouter(vpcs, peerings, events, routes, alloc)
	handler := auth.Middleware(router)

	slog.Info("starting vpc-interconnect-api", "port", cfg.Port, "regions", len(cfg.Regions))
	if err := http.ListenAndServe(fmt.Sprintf(":%s", cfg.Port), handler); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
