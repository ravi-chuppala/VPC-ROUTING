package bgp

import (
	"fmt"
	"net/netip"
)

// EVPNType5Route represents an EVPN Type-5 (IP Prefix) route for injection into the fabric.
type EVPNType5Route struct {
	RD          string       // Route Distinguisher: <region-id>:<vpc-id>
	Prefix      netip.Prefix // IP prefix from VPC CIDR
	PrefixLen   int
	EthernetTag uint32       // Always 0 for Type-5
	GatewayIP   netip.Addr   // 0.0.0.0 for recursive resolution
	VNI         uint32       // Target VPC's L3 VNI
	RouteTarget string       // Export RT: target:<account-id>:<vpc-id>
}

// BuildType5Routes constructs EVPN Type-5 routes for a VPC's CIDRs.
func BuildType5Routes(vpcID, regionID, accountID string, cidrs []netip.Prefix, vni uint32) []EVPNType5Route {
	routes := make([]EVPNType5Route, 0, len(cidrs))
	rd := fmt.Sprintf("%s:%s", regionID, truncate(vpcID, 8))
	rt := fmt.Sprintf("target:%s:%s", truncate(accountID, 8), truncate(vpcID, 8))

	for _, cidr := range cidrs {
		routes = append(routes, EVPNType5Route{
			RD:          rd,
			Prefix:      cidr,
			PrefixLen:   cidr.Bits(),
			EthernetTag: 0,
			GatewayIP:   netip.IPv4Unspecified(),
			VNI:         vni,
			RouteTarget: rt,
		})
	}
	return routes
}

// truncate safely returns up to n characters of s.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// RTConfig represents a Route Target import/export configuration change.
type RTConfig struct {
	VRFName     string
	Action      RTAction // add or remove
	RouteTarget string
}

type RTAction string

const (
	RTActionAddImport    RTAction = "add_import"
	RTActionRemoveImport RTAction = "remove_import"
)

// BuildPeeringRTConfigs returns the RT configurations needed when creating a peering.
// For bidirectional peering between VPC-A and VPC-B:
//   - VPC-A's VRF imports VPC-B's export RT
//   - VPC-B's VRF imports VPC-A's export RT
func BuildPeeringRTConfigs(
	vrfA, vrfB string,
	exportRTA, exportRTB string,
	action RTAction,
) []RTConfig {
	return []RTConfig{
		{VRFName: vrfA, Action: action, RouteTarget: exportRTB},
		{VRFName: vrfB, Action: action, RouteTarget: exportRTA},
	}
}
