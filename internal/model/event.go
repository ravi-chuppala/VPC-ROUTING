package model

import (
	"time"

	"github.com/google/uuid"
)

type EventType string

const (
	EventPeeringCreated                   EventType = "peering_created"
	EventPeeringAccepted                  EventType = "peering_accepted"
	EventPeeringProvisioned               EventType = "peering_provisioned"
	EventPeeringDeleted                   EventType = "peering_deleted"
	EventRouteAdded                       EventType = "route_added"
	EventRouteWithdrawn                   EventType = "route_withdrawn"
	EventRouteFiltered                    EventType = "route_filtered"
	EventMaxPrefixLimitReached            EventType = "max_prefix_limit_reached"
	EventStateChanged                     EventType = "state_changed"
	EventCrossRegionConnectivityLost      EventType = "cross_region_connectivity_lost"
	EventCrossRegionConnectivityRestored  EventType = "cross_region_connectivity_restored"
	EventPolicyUpdated                    EventType = "policy_updated"
	EventBandwidthLimitExceeded           EventType = "bandwidth_limit_exceeded"
)

type PeeringEvent struct {
	ID        uuid.UUID
	PeeringID uuid.UUID
	Type      EventType
	Message   string
	Timestamp time.Time
}
