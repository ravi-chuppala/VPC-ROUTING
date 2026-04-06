package store

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

// MemoryVPCStore is an in-memory VPC store for unit testing.
type MemoryVPCStore struct {
	mu   sync.RWMutex
	vpcs map[uuid.UUID]*model.VPC
}

func NewMemoryVPCStore() *MemoryVPCStore {
	return &MemoryVPCStore{vpcs: make(map[uuid.UUID]*model.VPC)}
}

func (s *MemoryVPCStore) Create(_ context.Context, vpc *model.VPC) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.vpcs[vpc.ID]; exists {
		return model.ErrConflict("VPC already exists")
	}
	clone := *vpc
	s.vpcs[vpc.ID] = &clone
	return nil
}

func (s *MemoryVPCStore) Get(_ context.Context, id uuid.UUID) (*model.VPC, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	vpc, ok := s.vpcs[id]
	if !ok || vpc.State == model.VPCStateDeleted {
		return nil, model.ErrNotFound("VPC")
	}
	clone := *vpc
	return &clone, nil
}

func (s *MemoryVPCStore) List(_ context.Context, accountID uuid.UUID, regionID string, params ListParams) (*ListResult[model.VPC], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.VPC
	for _, v := range s.vpcs {
		if v.AccountID != accountID || v.State == model.VPCStateDeleted {
			continue
		}
		if regionID != "" && v.RegionID != regionID {
			continue
		}
		items = append(items, *v)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	total := len(items)
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > len(items) {
		pageSize = len(items)
	}
	return &ListResult[model.VPC]{Items: items[:pageSize], TotalCount: total}, nil
}

func (s *MemoryVPCStore) Delete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpc, ok := s.vpcs[id]
	if !ok {
		return model.ErrNotFound("VPC")
	}
	now := time.Now()
	vpc.State = model.VPCStateDeleted
	vpc.DeletedAt = &now
	return nil
}

func (s *MemoryVPCStore) FindOverlappingCIDR(_ context.Context, accountID uuid.UUID, cidrs []netip.Prefix) (*model.VPC, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.vpcs {
		if v.AccountID != accountID || v.State == model.VPCStateDeleted {
			continue
		}
		if model.CIDRsOverlap(v.CIDRBlocks, cidrs) {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *MemoryVPCStore) FindByName(_ context.Context, accountID uuid.UUID, regionID, name string) (*model.VPC, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.vpcs {
		if v.AccountID == accountID && v.RegionID == regionID && v.Name == name && v.State != model.VPCStateDeleted {
			clone := *v
			return &clone, nil
		}
	}
	return nil, nil
}

// MemoryPeeringStore is an in-memory peering store for unit testing.
type MemoryPeeringStore struct {
	mu       sync.RWMutex
	peerings map[uuid.UUID]*model.Peering
}

func NewMemoryPeeringStore() *MemoryPeeringStore {
	return &MemoryPeeringStore{peerings: make(map[uuid.UUID]*model.Peering)}
}

func (s *MemoryPeeringStore) Create(_ context.Context, p *model.Peering) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *p
	s.peerings[p.ID] = &clone
	return nil
}

func (s *MemoryPeeringStore) Get(_ context.Context, id uuid.UUID) (*model.Peering, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peerings[id]
	if !ok || p.State == model.PeeringStateDeleted {
		return nil, model.ErrNotFound("peering")
	}
	clone := *p
	return &clone, nil
}

func (s *MemoryPeeringStore) List(_ context.Context, accountID uuid.UUID, vpcID *uuid.UUID, state *model.PeeringState, params ListParams) (*ListResult[model.Peering], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.Peering
	for _, p := range s.peerings {
		if p.State == model.PeeringStateDeleted {
			continue
		}
		if p.AccountID != accountID {
			// Check if account owns either side (simplified — real impl queries VPC ownership)
			continue
		}
		if vpcID != nil && p.RequesterVPCID != *vpcID && p.AccepterVPCID != *vpcID {
			continue
		}
		if state != nil && p.State != *state {
			continue
		}
		items = append(items, *p)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	total := len(items)
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > len(items) {
		pageSize = len(items)
	}
	return &ListResult[model.Peering]{Items: items[:pageSize], TotalCount: total}, nil
}

func (s *MemoryPeeringStore) Update(_ context.Context, p *model.Peering) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.peerings[p.ID]; !ok {
		return model.ErrNotFound("peering")
	}
	clone := *p
	s.peerings[p.ID] = &clone
	return nil
}

func (s *MemoryPeeringStore) Delete(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.peerings[id]
	if !ok {
		return model.ErrNotFound("peering")
	}
	now := time.Now()
	p.State = model.PeeringStateDeleted
	p.DeletedAt = &now
	return nil
}

func (s *MemoryPeeringStore) FindByVPCs(_ context.Context, vpcA, vpcB uuid.UUID) (*model.Peering, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.peerings {
		if p.State == model.PeeringStateDeleted || p.State == model.PeeringStateRejected || p.State == model.PeeringStateExpired {
			continue
		}
		if (p.RequesterVPCID == vpcA && p.AccepterVPCID == vpcB) ||
			(p.RequesterVPCID == vpcB && p.AccepterVPCID == vpcA) {
			clone := *p
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *MemoryPeeringStore) CountByVPC(_ context.Context, vpcID uuid.UUID) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, p := range s.peerings {
		if p.State == model.PeeringStateDeleted || p.State == model.PeeringStateRejected || p.State == model.PeeringStateExpired {
			continue
		}
		if p.RequesterVPCID == vpcID || p.AccepterVPCID == vpcID {
			count++
		}
	}
	return count, nil
}

func (s *MemoryPeeringStore) ListByState(_ context.Context, state model.PeeringState) ([]model.Peering, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.Peering
	for _, p := range s.peerings {
		if p.State == state {
			items = append(items, *p)
		}
	}
	return items, nil
}

// MemoryEventStore is an in-memory event store for unit testing.
type MemoryEventStore struct {
	mu     sync.RWMutex
	events []model.PeeringEvent
}

func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{}
}

func (s *MemoryEventStore) Append(_ context.Context, event *model.PeeringEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, *event)
	return nil
}

func (s *MemoryEventStore) List(_ context.Context, peeringID uuid.UUID, params ListParams) (*ListResult[model.PeeringEvent], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.PeeringEvent
	for _, e := range s.events {
		if e.PeeringID == peeringID {
			items = append(items, e)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Timestamp.After(items[j].Timestamp)
	})
	total := len(items)
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > len(items) {
		pageSize = len(items)
	}
	return &ListResult[model.PeeringEvent]{Items: items[:pageSize], TotalCount: total}, nil
}

// MemoryRouteStore is an in-memory route store for unit testing.
type MemoryRouteStore struct {
	mu     sync.RWMutex
	routes []model.RouteEntry
}

func NewMemoryRouteStore() *MemoryRouteStore {
	return &MemoryRouteStore{}
}

func (s *MemoryRouteStore) Upsert(_ context.Context, route *model.RouteEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.routes {
		if r.PeeringID == route.PeeringID && r.Prefix == route.Prefix {
			s.routes[i] = *route
			return nil
		}
	}
	s.routes = append(s.routes, *route)
	return nil
}

func (s *MemoryRouteStore) Delete(_ context.Context, peeringID uuid.UUID, prefix netip.Prefix) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.routes {
		if r.PeeringID == peeringID && r.Prefix == prefix {
			s.routes = append(s.routes[:i], s.routes[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *MemoryRouteStore) ListByPeering(_ context.Context, peeringID uuid.UUID) ([]model.RouteEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.RouteEntry
	for _, r := range s.routes {
		if r.PeeringID == peeringID {
			items = append(items, r)
		}
	}
	return items, nil
}

func (s *MemoryRouteStore) ListByVPC(_ context.Context, vpcID uuid.UUID) ([]model.RouteEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var items []model.RouteEntry
	for _, r := range s.routes {
		if r.OriginVPCID == vpcID {
			items = append(items, r)
		}
	}
	return items, nil
}
