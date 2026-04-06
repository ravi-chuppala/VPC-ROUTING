package model

import (
	"net/netip"

	"github.com/google/uuid"
)

type RouteOrigin string

const (
	RouteOriginDirect  RouteOrigin = "direct"
	RouteOriginStatic  RouteOrigin = "static"
	RouteOriginTransit RouteOrigin = "transit"
)

type RouteState string

const (
	RouteStateActive    RouteState = "active"
	RouteStateWithdrawn RouteState = "withdrawn"
	RouteStateFiltered  RouteState = "filtered"
)

type RouteEntry struct {
	Prefix      netip.Prefix
	OriginVPCID uuid.UUID
	PeeringID   uuid.UUID
	NextHop     netip.Addr
	VNI         uint32
	Origin      RouteOrigin
	Preference  int
	State       RouteState
}

// PreferenceForOrigin returns a route preference value (lower = more preferred).
func PreferenceForOrigin(origin RouteOrigin) int {
	switch origin {
	case RouteOriginDirect:
		return 100
	case RouteOriginStatic:
		return 200
	case RouteOriginTransit:
		return 300
	default:
		return 1000
	}
}
