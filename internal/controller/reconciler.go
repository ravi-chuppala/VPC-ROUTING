package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

// Reconciler continuously ensures desired peering state matches actual fabric state.
type Reconciler struct {
	vpcs        store.VPCStore
	peerings    store.PeeringStore
	events      store.EventStore
	routes      store.RouteStore
	bgpSvc      bgp.Service
	provisioner *Provisioner
	interval    time.Duration
	stopCh      chan struct{}
}

func NewReconciler(
	vpcs store.VPCStore,
	peerings store.PeeringStore,
	events store.EventStore,
	routes store.RouteStore,
	bgpSvc bgp.Service,
	interval time.Duration,
) *Reconciler {
	return &Reconciler{
		vpcs:     vpcs,
		peerings: peerings,
		events:   events,
		routes:   routes,
		bgpSvc:   bgpSvc,
		provisioner: NewProvisioner(vpcs, peerings, events, routes, bgpSvc),
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins the reconciliation loop.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.reconcileAll(ctx)
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("controller reconciler started", "interval", r.interval)
}

// Stop halts the reconciliation loop.
func (r *Reconciler) Stop() {
	close(r.stopCh)
}

// ReconcileOnce runs a single reconciliation pass (for testing).
func (r *Reconciler) ReconcileOnce(ctx context.Context) *ReconcileReport {
	return r.reconcileAll(ctx)
}

// ReconcileReport summarizes reconciliation results.
type ReconcileReport struct {
	Provisioned     int
	Deprovisioned   int
	ExpiredPending  int
	FailedRetries   int
	Errors          []string
}

func (r *Reconciler) reconcileAll(ctx context.Context) *ReconcileReport {
	report := &ReconcileReport{}

	// 1. Provision peerings stuck in "provisioning" state
	provisioning, _ := r.peerings.ListByState(ctx, model.PeeringStateProvisioning)
	for _, p := range provisioning {
		if err := r.provisioner.ProvisionPeering(ctx, p.ID); err != nil {
			slog.Error("provision failed", "peering_id", p.ID, "error", err)
			report.Errors = append(report.Errors, err.Error())
		} else {
			report.Provisioned++
		}
	}

	// 2. Deprovision peerings in "deleting" state
	deleting, _ := r.peerings.ListByState(ctx, model.PeeringStateDeleting)
	for _, p := range deleting {
		if err := r.provisioner.DeprovisionPeering(ctx, p.ID); err != nil {
			slog.Error("deprovision failed", "peering_id", p.ID, "error", err)
			report.Errors = append(report.Errors, err.Error())
		} else {
			report.Deprovisioned++
		}
	}

	// 3. Expire pending peerings older than 7 days
	pending, _ := r.peerings.ListByState(ctx, model.PeeringStatePendingAcceptance)
	for _, p := range pending {
		if time.Since(p.CreatedAt) > 7*24*time.Hour {
			p.State = model.PeeringStateExpired
			r.peerings.Update(ctx, &p)
			report.ExpiredPending++
			slog.Info("peering expired", "peering_id", p.ID)
		}
	}

	// 4. Retry failed peerings (up to 10 retries via provisioning timeout tracking)
	failed, _ := r.peerings.ListByState(ctx, model.PeeringStateFailed)
	for _, p := range failed {
		if time.Since(p.CreatedAt) < 10*time.Minute { // only retry recent failures
			p.State = model.PeeringStateProvisioning
			r.peerings.Update(ctx, &p)
			if err := r.provisioner.ProvisionPeering(ctx, p.ID); err != nil {
				slog.Error("retry provision failed", "peering_id", p.ID, "error", err)
				report.Errors = append(report.Errors, err.Error())
			} else {
				report.FailedRetries++
			}
		}
	}

	if report.Provisioned > 0 || report.Deprovisioned > 0 || report.ExpiredPending > 0 {
		slog.Info("reconciliation pass complete",
			"provisioned", report.Provisioned,
			"deprovisioned", report.Deprovisioned,
			"expired", report.ExpiredPending,
			"retries", report.FailedRetries,
			"errors", len(report.Errors),
		)
	}

	return report
}
