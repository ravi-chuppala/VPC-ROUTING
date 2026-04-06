package agent

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
)

// VRFConfig represents a Linux VRF configuration.
type VRFConfig struct {
	Name    string
	TableID int
	VNI     uint32
	VTEPIP  netip.Addr
}

// RouteConfig represents a route to program in a VRF.
type RouteConfig struct {
	VRFName string
	Prefix  netip.Prefix
	NextHop netip.Addr
	VNI     uint32
}

// ACLRule represents a firewall rule for a peering.
type ACLRule struct {
	SrcPrefix    netip.Prefix
	DstPrefix    netip.Prefix
	Protocol     string // "tcp", "udp", "icmp", "any"
	SrcPortStart int
	SrcPortEnd   int
	DstPortStart int
	DstPortEnd   int
	Action       string // "allow", "deny"
}

// ACLConfig represents ACL rules applied to a peering on a VRF.
type ACLConfig struct {
	VRFName   string
	PeeringID string
	Rules     []ACLRule
}

// NetlinkManager abstracts Linux netlink operations for VRF/VXLAN/route programming.
// In production, this uses github.com/vishvananda/netlink.
// For testing, use InMemoryNetlink.
type NetlinkManager interface {
	CreateVRF(cfg VRFConfig) error
	DeleteVRF(name string) error
	AddRoute(cfg RouteConfig) error
	DeleteRoute(vrfName string, prefix netip.Prefix) error
	ListRoutes(vrfName string) ([]RouteConfig, error)
	ProgramACL(cfg ACLConfig) error
	ClearACL(vrfName, peeringID string) error
	ListVRFs() ([]VRFConfig, error)
}

// InMemoryNetlink is a test implementation of NetlinkManager.
type InMemoryNetlink struct {
	mu     sync.RWMutex
	vrfs   map[string]*VRFConfig
	routes map[string][]RouteConfig // vrfName -> routes
	acls   map[string][]ACLConfig   // vrfName -> ACL configs
}

func NewInMemoryNetlink() *InMemoryNetlink {
	return &InMemoryNetlink{
		vrfs:   make(map[string]*VRFConfig),
		routes: make(map[string][]RouteConfig),
		acls:   make(map[string][]ACLConfig),
	}
}

func (n *InMemoryNetlink) CreateVRF(cfg VRFConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.vrfs[cfg.Name]; exists {
		return fmt.Errorf("VRF %s already exists", cfg.Name)
	}
	n.vrfs[cfg.Name] = &cfg
	slog.Info("VRF created", "name", cfg.Name, "table", cfg.TableID, "vni", cfg.VNI)
	return nil
}

func (n *InMemoryNetlink) DeleteVRF(name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.vrfs[name]; !exists {
		return fmt.Errorf("VRF %s not found", name)
	}
	delete(n.vrfs, name)
	delete(n.routes, name)
	delete(n.acls, name)
	slog.Info("VRF deleted", "name", name)
	return nil
}

func (n *InMemoryNetlink) AddRoute(cfg RouteConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.vrfs[cfg.VRFName]; !exists {
		return fmt.Errorf("VRF %s not found", cfg.VRFName)
	}
	// Check for duplicate
	for _, r := range n.routes[cfg.VRFName] {
		if r.Prefix == cfg.Prefix {
			return fmt.Errorf("route %s already exists in VRF %s", cfg.Prefix, cfg.VRFName)
		}
	}
	n.routes[cfg.VRFName] = append(n.routes[cfg.VRFName], cfg)
	slog.Debug("route added", "vrf", cfg.VRFName, "prefix", cfg.Prefix, "nexthop", cfg.NextHop)
	return nil
}

func (n *InMemoryNetlink) DeleteRoute(vrfName string, prefix netip.Prefix) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	routes := n.routes[vrfName]
	for i, r := range routes {
		if r.Prefix == prefix {
			n.routes[vrfName] = append(routes[:i], routes[i+1:]...)
			slog.Debug("route deleted", "vrf", vrfName, "prefix", prefix)
			return nil
		}
	}
	return fmt.Errorf("route %s not found in VRF %s", prefix, vrfName)
}

func (n *InMemoryNetlink) ListRoutes(vrfName string) ([]RouteConfig, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	routes := n.routes[vrfName]
	result := make([]RouteConfig, len(routes))
	copy(result, routes)
	return result, nil
}

func (n *InMemoryNetlink) ProgramACL(cfg ACLConfig) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Remove existing ACL for this peering, then add new
	existing := n.acls[cfg.VRFName]
	var filtered []ACLConfig
	for _, a := range existing {
		if a.PeeringID != cfg.PeeringID {
			filtered = append(filtered, a)
		}
	}
	filtered = append(filtered, cfg)
	n.acls[cfg.VRFName] = filtered
	slog.Info("ACL programmed", "vrf", cfg.VRFName, "peering", cfg.PeeringID, "rules", len(cfg.Rules))
	return nil
}

func (n *InMemoryNetlink) ClearACL(vrfName, peeringID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	existing := n.acls[vrfName]
	var filtered []ACLConfig
	for _, a := range existing {
		if a.PeeringID != peeringID {
			filtered = append(filtered, a)
		}
	}
	n.acls[vrfName] = filtered
	slog.Info("ACL cleared", "vrf", vrfName, "peering", peeringID)
	return nil
}

func (n *InMemoryNetlink) ListVRFs() ([]VRFConfig, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	result := make([]VRFConfig, 0, len(n.vrfs))
	for _, v := range n.vrfs {
		result = append(result, *v)
	}
	return result, nil
}

// GetACLs returns ACL configs for a VRF (test helper).
func (n *InMemoryNetlink) GetACLs(vrfName string) []ACLConfig {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.acls[vrfName]
}
