package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ravi-chuppala/vpc-routing/internal/api"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Initialize in-memory stores (swap for PostgreSQL in production)
	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	alloc := vni.NewAllocator()
	alloc.RegisterRegion("us-east-1", 0)
	alloc.RegisterRegion("us-west-1", 1)
	alloc.RegisterRegion("eu-west-1", 2)

	router := api.NewRouter(vpcs, peerings, events, routes, alloc)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Wrap router with auth middleware — validates Authorization header on all non-health endpoints
	handler := auth.Middleware(router)

	slog.Info("starting vpc-interconnect-api", "port", port)
	if err := http.ListenAndServe(fmt.Sprintf(":%s", port), handler); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}
