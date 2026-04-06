package agent

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestInMemoryNetlink_VRFLifecycle(t *testing.T) {
	nl := NewInMemoryNetlink()

	// Create VRF
	err := nl.CreateVRF(VRFConfig{Name: "vpc-aaaa", TableID: 100, VNI: 50000})
	if err != nil {
		t.Fatalf("CreateVRF() error: %v", err)
	}

	// Duplicate creation fails
	err = nl.CreateVRF(VRFConfig{Name: "vpc-aaaa", TableID: 100, VNI: 50000})
	if err == nil {
		t.Error("expected error for duplicate VRF")
	}

	// List VRFs
	vrfs, _ := nl.ListVRFs()
	if len(vrfs) != 1 {
		t.Fatalf("expected 1 VRF, got %d", len(vrfs))
	}

	// Delete VRF
	err = nl.DeleteVRF("vpc-aaaa")
	if err != nil {
		t.Fatalf("DeleteVRF() error: %v", err)
	}

	vrfs, _ = nl.ListVRFs()
	if len(vrfs) != 0 {
		t.Errorf("expected 0 VRFs after delete, got %d", len(vrfs))
	}
}

func TestInMemoryNetlink_Routes(t *testing.T) {
	nl := NewInMemoryNetlink()
	nl.CreateVRF(VRFConfig{Name: "vpc-aaaa", TableID: 100, VNI: 50000})

	// Add route
	err := nl.AddRoute(RouteConfig{
		VRFName: "vpc-aaaa",
		Prefix:  netip.MustParsePrefix("10.1.0.0/16"),
		NextHop: netip.MustParseAddr("192.168.1.1"),
		VNI:     60000,
	})
	if err != nil {
		t.Fatalf("AddRoute() error: %v", err)
	}

	// List routes
	routes, _ := nl.ListRoutes("vpc-aaaa")
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Prefix != netip.MustParsePrefix("10.1.0.0/16") {
		t.Errorf("prefix = %v, want 10.1.0.0/16", routes[0].Prefix)
	}

	// Duplicate route fails
	err = nl.AddRoute(RouteConfig{
		VRFName: "vpc-aaaa",
		Prefix:  netip.MustParsePrefix("10.1.0.0/16"),
	})
	if err == nil {
		t.Error("expected error for duplicate route")
	}

	// Delete route
	err = nl.DeleteRoute("vpc-aaaa", netip.MustParsePrefix("10.1.0.0/16"))
	if err != nil {
		t.Fatalf("DeleteRoute() error: %v", err)
	}

	routes, _ = nl.ListRoutes("vpc-aaaa")
	if len(routes) != 0 {
		t.Errorf("expected 0 routes after delete, got %d", len(routes))
	}

	// Route to non-existent VRF fails
	err = nl.AddRoute(RouteConfig{VRFName: "no-such-vrf", Prefix: netip.MustParsePrefix("10.0.0.0/16")})
	if err == nil {
		t.Error("expected error for non-existent VRF")
	}
}

func TestInMemoryNetlink_ACL(t *testing.T) {
	nl := NewInMemoryNetlink()
	nl.CreateVRF(VRFConfig{Name: "vpc-aaaa", TableID: 100, VNI: 50000})

	acl := ACLConfig{
		VRFName:   "vpc-aaaa",
		PeeringID: "peer-1",
		Rules: []ACLRule{
			{
				SrcPrefix: netip.MustParsePrefix("10.0.0.0/16"),
				DstPrefix: netip.MustParsePrefix("10.1.0.0/16"),
				Protocol:  "tcp",
				Action:    "allow",
			},
		},
	}

	err := nl.ProgramACL(acl)
	if err != nil {
		t.Fatalf("ProgramACL() error: %v", err)
	}

	acls := nl.GetACLs("vpc-aaaa")
	if len(acls) != 1 {
		t.Fatalf("expected 1 ACL config, got %d", len(acls))
	}
	if len(acls[0].Rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(acls[0].Rules))
	}

	// Clear ACL
	err = nl.ClearACL("vpc-aaaa", "peer-1")
	if err != nil {
		t.Fatalf("ClearACL() error: %v", err)
	}

	acls = nl.GetACLs("vpc-aaaa")
	if len(acls) != 0 {
		t.Errorf("expected 0 ACL configs after clear, got %d", len(acls))
	}
}

func TestReconciler_FixesDrift(t *testing.T) {
	nl := NewInMemoryNetlink()
	reconciler := NewReconciler(nl, 1*time.Second)

	// Set desired state: 1 VRF with 2 routes
	reconciler.SetDesiredState(DesiredState{
		VRFs: []VRFConfig{{Name: "vpc-aaaa", TableID: 100, VNI: 50000}},
		Routes: map[string][]RouteConfig{
			"vpc-aaaa": {
				{VRFName: "vpc-aaaa", Prefix: netip.MustParsePrefix("10.1.0.0/16"), NextHop: netip.MustParseAddr("192.168.1.1")},
				{VRFName: "vpc-aaaa", Prefix: netip.MustParsePrefix("10.2.0.0/16"), NextHop: netip.MustParseAddr("192.168.1.2")},
			},
		},
		ACLs: make(map[string][]ACLConfig),
	})

	// Run reconciliation
	report := reconciler.RunOnce()

	if report.VRFsCreated != 1 {
		t.Errorf("VRFs created = %d, want 1", report.VRFsCreated)
	}
	if report.RoutesAdded != 2 {
		t.Errorf("Routes added = %d, want 2", report.RoutesAdded)
	}

	// Verify state
	vrfs, _ := nl.ListVRFs()
	if len(vrfs) != 1 {
		t.Errorf("expected 1 VRF, got %d", len(vrfs))
	}
	routes, _ := nl.ListRoutes("vpc-aaaa")
	if len(routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(routes))
	}

	// Simulate drift: delete a route
	nl.DeleteRoute("vpc-aaaa", netip.MustParsePrefix("10.1.0.0/16"))

	// Reconcile again — should re-add the missing route
	report = reconciler.RunOnce()
	if report.RoutesAdded != 1 {
		t.Errorf("Routes re-added after drift = %d, want 1", report.RoutesAdded)
	}

	routes, _ = nl.ListRoutes("vpc-aaaa")
	if len(routes) != 2 {
		t.Errorf("expected 2 routes after re-reconcile, got %d", len(routes))
	}
}

func TestReconciler_StartStop(t *testing.T) {
	nl := NewInMemoryNetlink()
	reconciler := NewReconciler(nl, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	reconciler.Start(ctx)

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)
	cancel()
}
