package bgp

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
)

// PeerState represents the state of a BGP peer session.
type PeerState string

const (
	PeerStateIdle        PeerState = "idle"
	PeerStateConnect     PeerState = "connect"
	PeerStateActive      PeerState = "active"
	PeerStateOpenSent    PeerState = "opensent"
	PeerStateOpenConfirm PeerState = "openconfirm"
	PeerStateEstablished PeerState = "established"
)

// PeerInfo holds information about a BGP peer.
type PeerInfo struct {
	Address          string
	ASN              uint32
	State            PeerState
	UptimeSeconds    int64
	RoutesReceived   int64
	RoutesAdvertised int64
}

// RouteTableEntry represents a route in the BGP RIB.
type RouteTableEntry struct {
	Prefix      netip.Prefix
	NextHop     netip.Addr
	VNI         uint32
	RouteTarget string
	State       string // "active", "stale", "withdrawn"
}

// Service is the BGP control plane service interface.
// In production this wraps an embedded GoBGP server.
// For testing and development, use the InMemoryService.
type Service interface {
	// ConfigureRT adds or removes an import RT on a VRF.
	ConfigureRT(ctx context.Context, config RTConfig) error

	// InjectRoutes injects EVPN Type-5 routes into the fabric.
	InjectRoutes(ctx context.Context, vpcID string, routes []EVPNType5Route) (int, error)

	// WithdrawRoutes withdraws previously advertised routes.
	WithdrawRoutes(ctx context.Context, vpcID string, prefixes []netip.Prefix) (int, error)

	// GetRouteTable returns the current RIB for a VRF.
	GetRouteTable(ctx context.Context, vrfName string) ([]RouteTableEntry, error)

	// GetPeerStatus returns the status of all BGP peers.
	GetPeerStatus(ctx context.Context) ([]PeerInfo, error)

	// Start starts the BGP service.
	Start(ctx context.Context) error

	// Stop gracefully stops the BGP service.
	Stop() error
}

// Config holds BGP service configuration.
type Config struct {
	LocalASN       uint32
	RouterID       string
	ListenPort     int
	RouteReflectors []string // IPs of fabric RRs to peer with
}

// InMemoryService is a test/development implementation of the BGP Service.
type InMemoryService struct {
	mu          sync.RWMutex
	config      Config
	routes      map[string][]EVPNType5Route // vpcID -> routes
	rtConfigs   map[string][]string         // vrfName -> imported RTs
	routeTable  map[string][]RouteTableEntry // vrfName -> entries
	peers       []PeerInfo
	running     bool
}

func NewInMemoryService(config Config) *InMemoryService {
	return &InMemoryService{
		config:     config,
		routes:     make(map[string][]EVPNType5Route),
		rtConfigs:  make(map[string][]string),
		routeTable: make(map[string][]RouteTableEntry),
	}
}

func (s *InMemoryService) Start(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = true

	// Simulate peer sessions with route reflectors
	s.peers = make([]PeerInfo, len(s.config.RouteReflectors))
	for i, rr := range s.config.RouteReflectors {
		s.peers[i] = PeerInfo{
			Address: rr,
			ASN:     s.config.LocalASN,
			State:   PeerStateEstablished,
		}
	}

	slog.Info("bgp service started",
		"asn", s.config.LocalASN,
		"router_id", s.config.RouterID,
		"peers", len(s.config.RouteReflectors),
	)
	return nil
}

func (s *InMemoryService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
	slog.Info("bgp service stopped")
	return nil
}

func (s *InMemoryService) ConfigureRT(_ context.Context, config RTConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch config.Action {
	case RTActionAddImport:
		s.rtConfigs[config.VRFName] = append(s.rtConfigs[config.VRFName], config.RouteTarget)
		slog.Info("RT configured", "vrf", config.VRFName, "action", config.Action, "rt", config.RouteTarget)
	case RTActionRemoveImport:
		rts := s.rtConfigs[config.VRFName]
		for i, rt := range rts {
			if rt == config.RouteTarget {
				s.rtConfigs[config.VRFName] = append(rts[:i], rts[i+1:]...)
				break
			}
		}
		slog.Info("RT removed", "vrf", config.VRFName, "action", config.Action, "rt", config.RouteTarget)
	default:
		return fmt.Errorf("unknown RT action: %s", config.Action)
	}
	return nil
}

func (s *InMemoryService) InjectRoutes(_ context.Context, vpcID string, routes []EVPNType5Route) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.routes[vpcID] = append(s.routes[vpcID], routes...)

	// Add to route table for each route
	for _, r := range routes {
		entry := RouteTableEntry{
			Prefix:      r.Prefix,
			NextHop:     netip.Addr{}, // would be VTEP in production
			VNI:         r.VNI,
			RouteTarget: r.RouteTarget,
			State:       "active",
		}
		s.routeTable[r.RD] = append(s.routeTable[r.RD], entry)
	}

	slog.Info("routes injected", "vpc", vpcID, "count", len(routes))
	return len(routes), nil
}

func (s *InMemoryService) WithdrawRoutes(_ context.Context, vpcID string, prefixes []netip.Prefix) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	withdrawn := 0
	existing := s.routes[vpcID]
	var remaining []EVPNType5Route

	prefixSet := make(map[netip.Prefix]bool)
	for _, p := range prefixes {
		prefixSet[p] = true
	}

	for _, r := range existing {
		if prefixSet[r.Prefix] {
			withdrawn++
		} else {
			remaining = append(remaining, r)
		}
	}
	s.routes[vpcID] = remaining

	slog.Info("routes withdrawn", "vpc", vpcID, "count", withdrawn)
	return withdrawn, nil
}

func (s *InMemoryService) GetRouteTable(_ context.Context, vrfName string) ([]RouteTableEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries := s.routeTable[vrfName]
	if entries == nil {
		return []RouteTableEntry{}, nil
	}
	result := make([]RouteTableEntry, len(entries))
	copy(result, entries)
	return result, nil
}

func (s *InMemoryService) GetPeerStatus(_ context.Context) ([]PeerInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]PeerInfo, len(s.peers))
	copy(result, s.peers)
	return result, nil
}

// GetInjectedRoutes returns injected routes for a VPC (test helper).
func (s *InMemoryService) GetInjectedRoutes(vpcID string) []EVPNType5Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.routes[vpcID]
}

// GetImportedRTs returns imported RTs for a VRF (test helper).
func (s *InMemoryService) GetImportedRTs(vrfName string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rtConfigs[vrfName]
}
