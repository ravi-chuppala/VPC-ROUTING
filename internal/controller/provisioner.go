package controller

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/bgp"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

// Provisioner handles peering provisioning and deprovisioning.
type Provisioner struct {
	vpcs     store.VPCStore
	peerings store.PeeringStore
	events   store.EventStore
	routes   store.RouteStore
	bgpSvc   bgp.Service
}

func NewProvisioner(
	vpcs store.VPCStore,
	peerings store.PeeringStore,
	events store.EventStore,
	routes store.RouteStore,
	bgpSvc bgp.Service,
) *Provisioner {
	return &Provisioner{
		vpcs:     vpcs,
		peerings: peerings,
		events:   events,
		routes:   routes,
		bgpSvc:   bgpSvc,
	}
}

// ProvisionPeering provisions a peering: configures RT import/export, injects routes.
func (p *Provisioner) ProvisionPeering(ctx context.Context, peeringID uuid.UUID) error {
	peering, err := p.peerings.Get(ctx, peeringID)
	if err != nil {
		return fmt.Errorf("get peering: %w", err)
	}

	if peering.State != model.PeeringStateProvisioning {
		return fmt.Errorf("peering %s is in state %s, expected provisioning", peeringID, peering.State)
	}

	requester, err := p.vpcs.Get(ctx, peering.RequesterVPCID)
	if err != nil {
		return fmt.Errorf("get requester VPC: %w", err)
	}
	accepter, err := p.vpcs.Get(ctx, peering.AccepterVPCID)
	if err != nil {
		return fmt.Errorf("get accepter VPC: %w", err)
	}

	// Step 1: Configure RT import/export
	rtConfigs := bgp.BuildPeeringRTConfigs(
		requester.VRFName, accepter.VRFName,
		requester.ExportRT, accepter.ExportRT,
		bgp.RTActionAddImport,
	)
	for _, rtCfg := range rtConfigs {
		if err := p.bgpSvc.ConfigureRT(ctx, rtCfg); err != nil {
			return fmt.Errorf("configure RT: %w", err)
		}
	}

	// Step 2: Inject routes
	accepterRoutes := bgp.BuildType5Routes(
		accepter.ID.String(), accepter.RegionID, accepter.AccountID.String(),
		accepter.CIDRBlocks, accepter.VNI,
	)
	if _, err := p.bgpSvc.InjectRoutes(ctx, accepter.ID.String(), accepterRoutes); err != nil {
		return fmt.Errorf("inject accepter routes: %w", err)
	}

	if peering.Direction == model.PeeringDirectionBidirectional || peering.Direction == model.PeeringDirectionAccepterToRequester {
		requesterRoutes := bgp.BuildType5Routes(
			requester.ID.String(), requester.RegionID, requester.AccountID.String(),
			requester.CIDRBlocks, requester.VNI,
		)
		if _, err := p.bgpSvc.InjectRoutes(ctx, requester.ID.String(), requesterRoutes); err != nil {
			return fmt.Errorf("inject requester routes: %w", err)
		}
	}

	// Step 3: Store routes in database
	routeCount := 0
	for _, cidr := range accepter.CIDRBlocks {
		p.routes.Upsert(ctx, &model.RouteEntry{
			Prefix:      cidr,
			OriginVPCID: accepter.ID,
			PeeringID:   peeringID,
			Origin:      model.RouteOriginDirect,
			Preference:  model.PreferenceForOrigin(model.RouteOriginDirect),
			State:       model.RouteStateActive,
		})
		routeCount++
	}
	if peering.Direction == model.PeeringDirectionBidirectional || peering.Direction == model.PeeringDirectionAccepterToRequester {
		for _, cidr := range requester.CIDRBlocks {
			p.routes.Upsert(ctx, &model.RouteEntry{
				Prefix:      cidr,
				OriginVPCID: requester.ID,
				PeeringID:   peeringID,
				Origin:      model.RouteOriginDirect,
				Preference:  model.PreferenceForOrigin(model.RouteOriginDirect),
				State:       model.RouteStateActive,
			})
			routeCount++
		}
	}

	// Step 4: Transition to active
	now := time.Now()
	peering.State = model.PeeringStateActive
	peering.ProvisionedAt = &now
	peering.RouteCount = routeCount
	if err := p.peerings.Update(ctx, peering); err != nil {
		return fmt.Errorf("update peering state: %w", err)
	}

	p.emitEvent(ctx, peeringID, model.EventPeeringProvisioned,
		fmt.Sprintf("Peering is now active. %d routes installed.", routeCount))

	slog.Info("peering provisioned", "peering_id", peeringID, "routes", routeCount)
	return nil
}

// DeprovisionPeering tears down a peering: withdraws routes, removes RT import/export.
func (p *Provisioner) DeprovisionPeering(ctx context.Context, peeringID uuid.UUID) error {
	peering, err := p.peerings.Get(ctx, peeringID)
	if err != nil {
		return fmt.Errorf("get peering: %w", err)
	}

	requester, _ := p.vpcs.Get(ctx, peering.RequesterVPCID)
	accepter, _ := p.vpcs.Get(ctx, peering.AccepterVPCID)

	// Withdraw routes
	if accepter != nil {
		prefixes := make([]netip.Prefix, len(accepter.CIDRBlocks))
		for i, c := range accepter.CIDRBlocks {
			prefixes[i] = c
		}
		p.bgpSvc.WithdrawRoutes(ctx, accepter.ID.String(), prefixes)
	}
	if requester != nil {
		prefixes := make([]netip.Prefix, len(requester.CIDRBlocks))
		for i, c := range requester.CIDRBlocks {
			prefixes[i] = c
		}
		p.bgpSvc.WithdrawRoutes(ctx, requester.ID.String(), prefixes)
	}

	// Remove RT import/export
	if requester != nil && accepter != nil {
		rtConfigs := bgp.BuildPeeringRTConfigs(
			requester.VRFName, accepter.VRFName,
			requester.ExportRT, accepter.ExportRT,
			bgp.RTActionRemoveImport,
		)
		for _, rtCfg := range rtConfigs {
			p.bgpSvc.ConfigureRT(ctx, rtCfg)
		}
	}

	slog.Info("peering deprovisioned", "peering_id", peeringID)
	return nil
}

func (p *Provisioner) emitEvent(ctx context.Context, peeringID uuid.UUID, eventType model.EventType, message string) {
	p.events.Append(ctx, &model.PeeringEvent{
		ID:        uuid.New(),
		PeeringID: peeringID,
		Type:      eventType,
		Message:   message,
		Timestamp: time.Now(),
	})
}

