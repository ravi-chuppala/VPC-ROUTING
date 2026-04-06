package controller

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

func newTestEnv(t *testing.T) (*Provisioner, *Reconciler, *bgp.InMemoryService, store.VPCStore, store.PeeringStore) {
	t.Helper()
	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	bgpSvc := bgp.NewInMemoryService(bgp.Config{
		LocalASN:        65000,
		RouterID:        "10.0.0.1",
		RouteReflectors: []string{"10.0.0.100"},
	})
	bgpSvc.Start(context.Background())

	provisioner := NewProvisioner(vpcs, peerings, events, routes, bgpSvc)
	reconciler := NewReconciler(vpcs, peerings, events, routes, bgpSvc, 1*time.Second)

	return provisioner, reconciler, bgpSvc, vpcs, peerings
}

func createTestVPC(t *testing.T, vpcs store.VPCStore, accountID uuid.UUID, name, region, cidr string) *model.VPC {
	t.Helper()
	id := uuid.New()
	vpc := &model.VPC{
		ID:         id,
		AccountID:  accountID,
		RegionID:   region,
		Name:       name,
		CIDRBlocks: []netip.Prefix{netip.MustParsePrefix(cidr)},
		VNI:        50000 + uint32(id[0]),
		VRFName:    "vpc-" + id.String()[:8],
		RD:         region + ":" + id.String()[:8],
		ExportRT:   "target:" + accountID.String()[:8] + ":" + id.String()[:8],
		State:      model.VPCStateActive,
		CreatedAt:  time.Now(),
	}
	if err := vpcs.Create(context.Background(), vpc); err != nil {
		t.Fatalf("create VPC: %v", err)
	}
	return vpc
}

func TestProvisionPeering(t *testing.T) {
	provisioner, _, bgpSvc, vpcs, peerings := newTestEnv(t)
	ctx := context.Background()
	accountID := uuid.New()

	vpcA := createTestVPC(t, vpcs, accountID, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpcB := createTestVPC(t, vpcs, accountID, "vpc-b", "us-east-1", "10.1.0.0/16")

	peering := &model.Peering{
		ID:             uuid.New(),
		AccountID:      accountID,
		RequesterVPCID: vpcA.ID,
		AccepterVPCID:  vpcB.ID,
		Direction:      model.PeeringDirectionBidirectional,
		State:          model.PeeringStateProvisioning,
		RoutePolicy:    model.DefaultRoutePolicy(),
		CreatedAt:      time.Now(),
	}
	peerings.Create(ctx, peering)

	err := provisioner.ProvisionPeering(ctx, peering.ID)
	if err != nil {
		t.Fatalf("ProvisionPeering() error: %v", err)
	}

	// Verify peering state is active
	updated, _ := peerings.Get(ctx, peering.ID)
	if updated.State != model.PeeringStateActive {
		t.Errorf("state = %s, want active", updated.State)
	}
	if updated.ProvisionedAt == nil {
		t.Error("provisioned_at should be set")
	}
	if updated.RouteCount < 2 {
		t.Errorf("route_count = %d, want >= 2", updated.RouteCount)
	}

	// Verify BGP routes were injected
	injectedA := bgpSvc.GetInjectedRoutes(vpcA.ID.String())
	injectedB := bgpSvc.GetInjectedRoutes(vpcB.ID.String())
	if len(injectedA) == 0 {
		t.Error("expected routes injected for VPC-A")
	}
	if len(injectedB) == 0 {
		t.Error("expected routes injected for VPC-B")
	}

	// Verify RT was configured
	rtsA := bgpSvc.GetImportedRTs(vpcA.VRFName)
	rtsB := bgpSvc.GetImportedRTs(vpcB.VRFName)
	if len(rtsA) != 1 {
		t.Errorf("VPC-A imported RTs = %d, want 1", len(rtsA))
	}
	if len(rtsB) != 1 {
		t.Errorf("VPC-B imported RTs = %d, want 1", len(rtsB))
	}
}

func TestDeprovisionPeering(t *testing.T) {
	provisioner, _, bgpSvc, vpcs, peerings := newTestEnv(t)
	ctx := context.Background()
	accountID := uuid.New()

	vpcA := createTestVPC(t, vpcs, accountID, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpcB := createTestVPC(t, vpcs, accountID, "vpc-b", "us-east-1", "10.1.0.0/16")

	peering := &model.Peering{
		ID:             uuid.New(),
		AccountID:      accountID,
		RequesterVPCID: vpcA.ID,
		AccepterVPCID:  vpcB.ID,
		Direction:      model.PeeringDirectionBidirectional,
		State:          model.PeeringStateProvisioning,
		RoutePolicy:    model.DefaultRoutePolicy(),
		CreatedAt:      time.Now(),
	}
	peerings.Create(ctx, peering)
	provisioner.ProvisionPeering(ctx, peering.ID)

	// Now deprovision
	err := provisioner.DeprovisionPeering(ctx, peering.ID)
	if err != nil {
		t.Fatalf("DeprovisionPeering() error: %v", err)
	}

	// Verify routes withdrawn
	remaining := bgpSvc.GetInjectedRoutes(vpcB.ID.String())
	if len(remaining) != 0 {
		t.Errorf("expected 0 remaining routes for VPC-B, got %d", len(remaining))
	}

	// Verify RT removed
	rtsA := bgpSvc.GetImportedRTs(vpcA.VRFName)
	if len(rtsA) != 0 {
		t.Errorf("expected 0 imported RTs for VPC-A, got %d", len(rtsA))
	}
}

func TestReconciler_ProvisionsPending(t *testing.T) {
	_, reconciler, _, vpcs, peerings := newTestEnv(t)
	ctx := context.Background()
	accountID := uuid.New()

	vpcA := createTestVPC(t, vpcs, accountID, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpcB := createTestVPC(t, vpcs, accountID, "vpc-b", "us-east-1", "10.1.0.0/16")

	peering := &model.Peering{
		ID:             uuid.New(),
		AccountID:      accountID,
		RequesterVPCID: vpcA.ID,
		AccepterVPCID:  vpcB.ID,
		Direction:      model.PeeringDirectionBidirectional,
		State:          model.PeeringStateProvisioning,
		RoutePolicy:    model.DefaultRoutePolicy(),
		CreatedAt:      time.Now(),
	}
	peerings.Create(ctx, peering)

	report := reconciler.ReconcileOnce(ctx)
	if report.Provisioned != 1 {
		t.Errorf("provisioned = %d, want 1", report.Provisioned)
	}

	updated, _ := peerings.Get(ctx, peering.ID)
	if updated.State != model.PeeringStateActive {
		t.Errorf("state = %s, want active", updated.State)
	}
}

func TestReconciler_ExpiresPending(t *testing.T) {
	_, reconciler, _, vpcs, peerings := newTestEnv(t)
	ctx := context.Background()
	accountID := uuid.New()
	otherAccount := uuid.New()

	vpcA := createTestVPC(t, vpcs, accountID, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpcB := createTestVPC(t, vpcs, otherAccount, "vpc-b", "us-east-1", "10.1.0.0/16")

	peering := &model.Peering{
		ID:             uuid.New(),
		AccountID:      accountID,
		RequesterVPCID: vpcA.ID,
		AccepterVPCID:  vpcB.ID,
		Direction:      model.PeeringDirectionBidirectional,
		State:          model.PeeringStatePendingAcceptance,
		RoutePolicy:    model.DefaultRoutePolicy(),
		CreatedAt:      time.Now().Add(-8 * 24 * time.Hour), // 8 days ago
	}
	peerings.Create(ctx, peering)

	report := reconciler.ReconcileOnce(ctx)
	if report.ExpiredPending != 1 {
		t.Errorf("expired = %d, want 1", report.ExpiredPending)
	}

	updated, _ := peerings.Get(ctx, peering.ID)
	if updated.State != model.PeeringStateExpired {
		t.Errorf("state = %s, want expired", updated.State)
	}
}
