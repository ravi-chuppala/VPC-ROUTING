package model

import (
	"time"

	"github.com/google/uuid"
)

type PeeringState string

const (
	PeeringStatePendingAcceptance PeeringState = "pending_acceptance"
	PeeringStateProvisioning      PeeringState = "provisioning"
	PeeringStateActive            PeeringState = "active"
	PeeringStateDegraded          PeeringState = "degraded"
	PeeringStateDeleting          PeeringState = "deleting"
	PeeringStateFailed            PeeringState = "failed"
	PeeringStateRejected          PeeringState = "rejected"
	PeeringStateExpired           PeeringState = "expired"
	PeeringStateDeleted           PeeringState = "deleted"
)

type PeeringDirection string

const (
	PeeringDirectionBidirectional      PeeringDirection = "bidirectional"
	PeeringDirectionRequesterToAccepter PeeringDirection = "requester_to_accepter"
	PeeringDirectionAccepterToRequester PeeringDirection = "accepter_to_requester"
)

type Peering struct {
	ID             uuid.UUID
	AccountID      uuid.UUID
	RequesterVPCID uuid.UUID
	AccepterVPCID  uuid.UUID
	Direction      PeeringDirection
	State          PeeringState
	Health         PeeringHealth
	CrossRegion    bool
	LatencyMs      *float64
	RoutePolicy    RoutePolicy
	RouteCount     int
	CreatedAt      time.Time
	ProvisionedAt  *time.Time
	DeletedAt      *time.Time
}

type PeeringHealth string

const (
	PeeringHealthHealthy  PeeringHealth = "healthy"
	PeeringHealthDegraded PeeringHealth = "degraded"
	PeeringHealthDown     PeeringHealth = "down"
)

func ComputeHealth(p *Peering) PeeringHealth {
	if p.State != PeeringStateActive {
		return PeeringHealthDown
	}
	if p.State == PeeringStateActive && p.RouteCount == 0 {
		return PeeringHealthDegraded
	}
	return PeeringHealthHealthy
}
