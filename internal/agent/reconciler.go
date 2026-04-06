package agent

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"
	"time"
)

// DesiredState represents what the controller wants programmed on a host.
type DesiredState struct {
	VRFs   []VRFConfig
	Routes map[string][]RouteConfig // vrfName -> routes
	ACLs   map[string][]ACLConfig   // vrfName -> ACLs
}

// Reconciler periodically compares desired state with actual kernel state and corrects drift.
type Reconciler struct {
	netlink  NetlinkManager
	mu       sync.RWMutex
	desired  DesiredState
	interval time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewReconciler(netlink NetlinkManager, interval time.Duration) *Reconciler {
	return &Reconciler{
		netlink:  netlink,
		interval: interval,
		desired: DesiredState{
			Routes: make(map[string][]RouteConfig),
			ACLs:   make(map[string][]ACLConfig),
		},
		stopCh: make(chan struct{}),
	}
}

// SetDesiredState updates the desired state from the controller.
func (r *Reconciler) SetDesiredState(state DesiredState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desired = state
}

// AddDesiredVRF adds a VRF to desired state.
func (r *Reconciler) AddDesiredVRF(vrf VRFConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desired.VRFs = append(r.desired.VRFs, vrf)
}

// AddDesiredRoutes adds routes for a VRF to desired state.
func (r *Reconciler) AddDesiredRoutes(vrfName string, routes []RouteConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.desired.Routes[vrfName] = append(r.desired.Routes[vrfName], routes...)
}

// Start begins the reconciliation loop.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.reconcile()
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop halts the reconciliation loop. Safe to call multiple times.
func (r *Reconciler) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// snapshotDesired creates a deep copy of the desired state. Caller must hold r.mu.
func (r *Reconciler) snapshotDesired() DesiredState {
	snap := DesiredState{
		VRFs:   make([]VRFConfig, len(r.desired.VRFs)),
		Routes: make(map[string][]RouteConfig, len(r.desired.Routes)),
		ACLs:   make(map[string][]ACLConfig, len(r.desired.ACLs)),
	}
	copy(snap.VRFs, r.desired.VRFs)
	for k, v := range r.desired.Routes {
		routes := make([]RouteConfig, len(v))
		copy(routes, v)
		snap.Routes[k] = routes
	}
	for k, v := range r.desired.ACLs {
		acls := make([]ACLConfig, len(v))
		copy(acls, v)
		snap.ACLs[k] = acls
	}
	return snap
}

// RunOnce performs a single reconciliation pass.
func (r *Reconciler) RunOnce() *ReconcileReport {
	return r.reconcile()
}

// ReconcileReport summarizes what the reconciler did.
type ReconcileReport struct {
	VRFsCreated   int
	VRFsDeleted   int
	RoutesAdded   int
	RoutesRemoved int
	ACLsUpdated   int
	Errors        []string
}

func (r *Reconciler) reconcile() *ReconcileReport {
	r.mu.RLock()
	desired := r.snapshotDesired()
	r.mu.RUnlock()

	report := &ReconcileReport{}

	// Reconcile VRFs
	actualVRFs, _ := r.netlink.ListVRFs()
	actualVRFMap := make(map[string]bool)
	for _, v := range actualVRFs {
		actualVRFMap[v.Name] = true
	}
	desiredVRFMap := make(map[string]bool)
	for _, v := range desired.VRFs {
		desiredVRFMap[v.Name] = true
		if !actualVRFMap[v.Name] {
			if err := r.netlink.CreateVRF(v); err != nil {
				report.Errors = append(report.Errors, err.Error())
			} else {
				report.VRFsCreated++
			}
		}
	}

	// Delete stale VRFs (exist on host but not in desired state)
	for _, v := range actualVRFs {
		if !desiredVRFMap[v.Name] {
			if err := r.netlink.DeleteVRF(v.Name); err != nil {
				report.Errors = append(report.Errors, err.Error())
			} else {
				report.VRFsDeleted++
			}
		}
	}

	// Reconcile routes per VRF
	for vrfName, desiredRoutes := range desired.Routes {
		actualRoutes, _ := r.netlink.ListRoutes(vrfName)
		actualPrefixes := make(map[netip.Prefix]bool)
		for _, ar := range actualRoutes {
			actualPrefixes[ar.Prefix] = true
		}

		desiredPrefixes := make(map[netip.Prefix]bool)
		for _, dr := range desiredRoutes {
			desiredPrefixes[dr.Prefix] = true
			if !actualPrefixes[dr.Prefix] {
				if err := r.netlink.AddRoute(dr); err != nil {
					report.Errors = append(report.Errors, err.Error())
				} else {
					report.RoutesAdded++
				}
			}
		}

		// Remove stale routes
		for _, ar := range actualRoutes {
			if !desiredPrefixes[ar.Prefix] {
				if err := r.netlink.DeleteRoute(vrfName, ar.Prefix); err != nil {
					report.Errors = append(report.Errors, err.Error())
				} else {
					report.RoutesRemoved++
				}
			}
		}
	}

	// Reconcile ACLs
	for vrfName, desiredACLs := range desired.ACLs {
		for _, acl := range desiredACLs {
			if err := r.netlink.ProgramACL(acl); err != nil {
				report.Errors = append(report.Errors, err.Error())
			} else {
				report.ACLsUpdated++
			}
		}
		_ = vrfName
	}

	if report.VRFsCreated > 0 || report.RoutesAdded > 0 || report.RoutesRemoved > 0 {
		slog.Info("reconciliation complete",
			"vrfs_created", report.VRFsCreated,
			"routes_added", report.RoutesAdded,
			"routes_removed", report.RoutesRemoved,
			"acls_updated", report.ACLsUpdated,
			"errors", len(report.Errors),
		)
	}

	return report
}
