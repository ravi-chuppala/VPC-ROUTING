package bgp

import (
	"context"
	"net/netip"
	"testing"
)

func TestBuildType5Routes(t *testing.T) {
	cidrs := []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/16"),
		netip.MustParsePrefix("10.1.0.0/16"),
	}

	routes := BuildType5Routes("a1b2c3d4-e5f6", "us-east-1", "acct-001-xxxx", cidrs, 100001)

	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}

	r := routes[0]
	if r.RD != "us-east-1:a1b2c3d4" {
		t.Errorf("RD = %s, want us-east-1:a1b2c3d4", r.RD)
	}
	if r.VNI != 100001 {
		t.Errorf("VNI = %d, want 100001", r.VNI)
	}
	if r.EthernetTag != 0 {
		t.Errorf("EthernetTag = %d, want 0", r.EthernetTag)
	}
	if r.Prefix != cidrs[0] {
		t.Errorf("Prefix = %v, want %v", r.Prefix, cidrs[0])
	}
	if r.RouteTarget != "target:acct-001:a1b2c3d4" {
		t.Errorf("RouteTarget = %s, want target:acct-001:a1b2c3d4", r.RouteTarget)
	}
}

func TestBuildPeeringRTConfigs(t *testing.T) {
	configs := BuildPeeringRTConfigs("vpc-aaaa", "vpc-bbbb", "target:a:a", "target:b:b", RTActionAddImport)

	if len(configs) != 2 {
		t.Fatalf("expected 2 RT configs, got %d", len(configs))
	}

	// VPC-A imports VPC-B's RT
	if configs[0].VRFName != "vpc-aaaa" || configs[0].RouteTarget != "target:b:b" {
		t.Errorf("config[0] = %+v, want vrf=vpc-aaaa rt=target:b:b", configs[0])
	}
	// VPC-B imports VPC-A's RT
	if configs[1].VRFName != "vpc-bbbb" || configs[1].RouteTarget != "target:a:a" {
		t.Errorf("config[1] = %+v, want vrf=vpc-bbbb rt=target:a:a", configs[1])
	}
}

func TestInMemoryService_InjectAndWithdraw(t *testing.T) {
	svc := NewInMemoryService(Config{
		LocalASN:        65000,
		RouterID:        "10.0.0.1",
		RouteReflectors: []string{"10.0.0.100", "10.0.0.101"},
	})
	ctx := context.Background()

	if err := svc.Start(ctx); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer svc.Stop()

	// Check peers
	peers, _ := svc.GetPeerStatus(ctx)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if peers[0].State != PeerStateEstablished {
		t.Errorf("peer state = %s, want established", peers[0].State)
	}

	// Inject routes
	routes := BuildType5Routes("vpc-1234-5678", "us-east-1", "acct-0001-0000", []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/16"),
	}, 50000)

	count, err := svc.InjectRoutes(ctx, "vpc-1234", routes)
	if err != nil {
		t.Fatalf("InjectRoutes() error: %v", err)
	}
	if count != 1 {
		t.Errorf("injected = %d, want 1", count)
	}

	injected := svc.GetInjectedRoutes("vpc-1234")
	if len(injected) != 1 {
		t.Fatalf("stored routes = %d, want 1", len(injected))
	}

	// Withdraw
	withdrawn, err := svc.WithdrawRoutes(ctx, "vpc-1234", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/16")})
	if err != nil {
		t.Fatalf("WithdrawRoutes() error: %v", err)
	}
	if withdrawn != 1 {
		t.Errorf("withdrawn = %d, want 1", withdrawn)
	}

	remaining := svc.GetInjectedRoutes("vpc-1234")
	if len(remaining) != 0 {
		t.Errorf("remaining routes = %d, want 0", len(remaining))
	}
}

func TestInMemoryService_ConfigureRT(t *testing.T) {
	svc := NewInMemoryService(Config{LocalASN: 65000, RouterID: "10.0.0.1"})
	ctx := context.Background()
	svc.Start(ctx)
	defer svc.Stop()

	// Add import RT
	err := svc.ConfigureRT(ctx, RTConfig{
		VRFName:     "vpc-aaaa",
		Action:      RTActionAddImport,
		RouteTarget: "target:b:b",
	})
	if err != nil {
		t.Fatalf("ConfigureRT add error: %v", err)
	}

	rts := svc.GetImportedRTs("vpc-aaaa")
	if len(rts) != 1 || rts[0] != "target:b:b" {
		t.Errorf("imported RTs = %v, want [target:b:b]", rts)
	}

	// Remove import RT
	err = svc.ConfigureRT(ctx, RTConfig{
		VRFName:     "vpc-aaaa",
		Action:      RTActionRemoveImport,
		RouteTarget: "target:b:b",
	})
	if err != nil {
		t.Fatalf("ConfigureRT remove error: %v", err)
	}

	rts = svc.GetImportedRTs("vpc-aaaa")
	if len(rts) != 0 {
		t.Errorf("imported RTs after remove = %v, want empty", rts)
	}
}
