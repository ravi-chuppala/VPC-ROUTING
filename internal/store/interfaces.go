package store

import (
	"context"
	"net/netip"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

type ListParams struct {
	PageSize  int
	PageToken string // opaque cursor
}

type ListResult[T any] struct {
	Items         []T
	NextPageToken string
	TotalCount    int
}

type VPCStore interface {
	Create(ctx context.Context, vpc *model.VPC) error
	Get(ctx context.Context, id uuid.UUID) (*model.VPC, error)
	List(ctx context.Context, accountID uuid.UUID, regionID string, params ListParams) (*ListResult[model.VPC], error)
	Delete(ctx context.Context, id uuid.UUID) error
	FindOverlappingCIDR(ctx context.Context, accountID uuid.UUID, cidrs []netip.Prefix) (*model.VPC, error)
	FindByName(ctx context.Context, accountID uuid.UUID, regionID, name string) (*model.VPC, error)
	CountPeerings(ctx context.Context, vpcID uuid.UUID) (int, error)
}

type PeeringStore interface {
	Create(ctx context.Context, peering *model.Peering) error
	Get(ctx context.Context, id uuid.UUID) (*model.Peering, error)
	List(ctx context.Context, accountID uuid.UUID, vpcID *uuid.UUID, state *model.PeeringState, params ListParams) (*ListResult[model.Peering], error)
	Update(ctx context.Context, peering *model.Peering) error
	Delete(ctx context.Context, id uuid.UUID) error
	FindByVPCs(ctx context.Context, vpcA, vpcB uuid.UUID) (*model.Peering, error)
	CountByVPC(ctx context.Context, vpcID uuid.UUID) (int, error)
	ListByState(ctx context.Context, state model.PeeringState) ([]model.Peering, error)
}

type EventStore interface {
	Append(ctx context.Context, event *model.PeeringEvent) error
	List(ctx context.Context, peeringID uuid.UUID, params ListParams) (*ListResult[model.PeeringEvent], error)
}

type RouteStore interface {
	Upsert(ctx context.Context, route *model.RouteEntry) error
	Delete(ctx context.Context, peeringID uuid.UUID, prefix netip.Prefix) error
	ListByPeering(ctx context.Context, peeringID uuid.UUID) ([]model.RouteEntry, error)
	ListByVPC(ctx context.Context, vpcID uuid.UUID) ([]model.RouteEntry, error)
}
