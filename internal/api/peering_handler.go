package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
)

const defaultMaxPeerings = 125

type PeeringHandler struct {
	vpcs     store.VPCStore
	peerings store.PeeringStore
	events   store.EventStore
	routes   store.RouteStore
}

func NewPeeringHandler(vpcs store.VPCStore, peerings store.PeeringStore, events store.EventStore, routes store.RouteStore) *PeeringHandler {
	return &PeeringHandler{vpcs: vpcs, peerings: peerings, events: events, routes: routes}
}

// --- Request/Response types ---

type CreatePeeringRequest struct {
	RequesterVPCID string              `json:"requester_vpc_id"`
	AccepterVPCID  string              `json:"accepter_vpc_id"`
	Direction      string              `json:"direction"`
	RoutePolicy    *RoutePolicyRequest `json:"route_policy"`
}

type RoutePolicyRequest struct {
	AllowedPrefixes    []string `json:"allowed_prefixes"`
	DeniedPrefixes     []string `json:"denied_prefixes"`
	MaxPrefixes        *int     `json:"max_prefixes"`
	BandwidthLimitMbps *int     `json:"bandwidth_limit_mbps"`
}

type UpdatePeeringRequest struct {
	Direction   *string             `json:"direction"`
	RoutePolicy *RoutePolicyRequest `json:"route_policy"`
}

type PeeringResponse struct {
	ID             string              `json:"id"`
	AccountID      string              `json:"account_id"`
	RequesterVPCID string              `json:"requester_vpc_id"`
	AccepterVPCID  string              `json:"accepter_vpc_id"`
	Direction      string              `json:"direction"`
	State          string              `json:"state"`
	Health         string              `json:"health"`
	CrossRegion    bool                `json:"cross_region"`
	LatencyMs      *float64            `json:"measured_latency_ms"`
	RoutePolicy    RoutePolicyResponse `json:"route_policy"`
	RouteCount     int                 `json:"route_count"`
	CreatedAt      string              `json:"created_at"`
	ProvisionedAt  *string             `json:"provisioned_at"`
}

type RoutePolicyResponse struct {
	AllowedPrefixes    []string `json:"allowed_prefixes"`
	DeniedPrefixes     []string `json:"denied_prefixes"`
	MaxPrefixes        int      `json:"max_prefixes"`
	BandwidthLimitMbps *int     `json:"bandwidth_limit_mbps"`
}

type OverrideRouteRequest struct {
	Action string `json:"action"` // "add_static" or "withdraw"
	Prefix string `json:"prefix"`
}

type RouteEntryResponse struct {
	Prefix      string `json:"prefix"`
	OriginVPCID string `json:"origin_vpc_id"`
	State       string `json:"state"`
	Origin      string `json:"origin"`
	NextHop     string `json:"next_hop"`
	Preference  int    `json:"preference"`
}

type EventResponse struct {
	ID        string `json:"id"`
	PeeringID string `json:"peering_id"`
	Type      string `json:"type"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

func peeringToResponse(p *model.Peering) *PeeringResponse {
	resp := &PeeringResponse{
		ID:             p.ID.String(),
		AccountID:      p.AccountID.String(),
		RequesterVPCID: p.RequesterVPCID.String(),
		AccepterVPCID:  p.AccepterVPCID.String(),
		Direction:      string(p.Direction),
		State:          string(p.State),
		Health:         string(model.ComputeHealth(p)),
		CrossRegion:    p.CrossRegion,
		LatencyMs:      p.LatencyMs,
		RoutePolicy:    policyToResponse(p.RoutePolicy),
		RouteCount:     p.RouteCount,
		CreatedAt:      p.CreatedAt.Format(time.RFC3339),
	}
	if p.ProvisionedAt != nil {
		t := p.ProvisionedAt.Format(time.RFC3339)
		resp.ProvisionedAt = &t
	}
	return resp
}

func policyToResponse(p model.RoutePolicy) RoutePolicyResponse {
	allowed := make([]string, len(p.AllowedPrefixes))
	for i, a := range p.AllowedPrefixes {
		allowed[i] = a.String()
	}
	denied := make([]string, len(p.DeniedPrefixes))
	for i, d := range p.DeniedPrefixes {
		denied[i] = d.String()
	}
	return RoutePolicyResponse{
		AllowedPrefixes:    allowed,
		DeniedPrefixes:     denied,
		MaxPrefixes:        p.MaxPrefixes,
		BandwidthLimitMbps: p.BandwidthLimitMbps,
	}
}

func parseRoutePolicy(req *RoutePolicyRequest) (model.RoutePolicy, error) {
	if req == nil {
		return model.DefaultRoutePolicy(), nil
	}
	policy := model.RoutePolicy{
		MaxPrefixes:        100,
		BandwidthLimitMbps: req.BandwidthLimitMbps,
	}
	if req.MaxPrefixes != nil {
		policy.MaxPrefixes = *req.MaxPrefixes
	}
	for _, s := range req.AllowedPrefixes {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return policy, model.ErrInvalidCIDR(fmt.Sprintf("invalid allowed prefix: %s", s))
		}
		policy.AllowedPrefixes = append(policy.AllowedPrefixes, p)
	}
	for _, s := range req.DeniedPrefixes {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return policy, model.ErrInvalidCIDR(fmt.Sprintf("invalid denied prefix: %s", s))
		}
		policy.DeniedPrefixes = append(policy.DeniedPrefixes, p)
	}
	return policy, nil
}

// --- Handlers ---

func (h *PeeringHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	var req CreatePeeringRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, model.ErrInvalidInput("invalid JSON body"))
		return
	}

	requesterID, err := uuid.Parse(req.RequesterVPCID)
	if err != nil {
		writeError(w, model.ErrInvalidInput("invalid requester_vpc_id"))
		return
	}
	accepterID, err := uuid.Parse(req.AccepterVPCID)
	if err != nil {
		writeError(w, model.ErrInvalidInput("invalid accepter_vpc_id"))
		return
	}
	if requesterID == accepterID {
		writeError(w, model.ErrInvalidInput("cannot peer a VPC with itself"))
		return
	}

	// Validate ownership of requester VPC
	requesterVPC, err := h.vpcs.Get(ctx, requesterID)
	if err != nil {
		writeError(w, err)
		return
	}
	if requesterVPC.AccountID != accountID {
		writeError(w, model.ErrPermissionDenied("caller does not own requester VPC"))
		return
	}

	// Validate accepter VPC exists
	accepterVPC, err := h.vpcs.Get(ctx, accepterID)
	if err != nil {
		writeError(w, err)
		return
	}

	// CIDR overlap check between VPCs
	if model.CIDRsOverlap(requesterVPC.CIDRBlocks, accepterVPC.CIDRBlocks) {
		writeError(w, model.ErrCIDROverlap("requester and accepter VPCs have overlapping CIDRs"))
		return
	}

	// Duplicate check
	existing, err := h.peerings.FindByVPCs(ctx, requesterID, accepterID)
	if err != nil {
		writeError(w, err)
		return
	}
	if existing != nil {
		writeError(w, model.ErrDuplicatePeering())
		return
	}

	// Quota check
	reqCount, _ := h.peerings.CountByVPC(ctx, requesterID)
	if reqCount >= defaultMaxPeerings {
		writeError(w, model.ErrQuotaExceeded(fmt.Sprintf("requester VPC has reached peering limit of %d", defaultMaxPeerings)))
		return
	}
	accCount, _ := h.peerings.CountByVPC(ctx, accepterID)
	if accCount >= defaultMaxPeerings {
		writeError(w, model.ErrQuotaExceeded(fmt.Sprintf("accepter VPC has reached peering limit of %d", defaultMaxPeerings)))
		return
	}

	// Parse direction
	direction := model.PeeringDirectionBidirectional
	if req.Direction != "" {
		direction = model.PeeringDirection(req.Direction)
	}

	// Parse route policy
	policy, err := parseRoutePolicy(req.RoutePolicy)
	if err != nil {
		writeError(w, err)
		return
	}

	// Determine initial state
	sameAccount := requesterVPC.AccountID == accepterVPC.AccountID
	state := model.PeeringStatePendingAcceptance
	if sameAccount {
		state = model.PeeringStateProvisioning
	}

	crossRegion := requesterVPC.RegionID != accepterVPC.RegionID

	peering := &model.Peering{
		ID:             uuid.New(),
		AccountID:      accountID,
		RequesterVPCID: requesterID,
		AccepterVPCID:  accepterID,
		Direction:      direction,
		State:          state,
		CrossRegion:    crossRegion,
		RoutePolicy:    policy,
		CreatedAt:      time.Now(),
	}

	if err := h.peerings.Create(ctx, peering); err != nil {
		writeError(w, err)
		return
	}

	// Emit event
	h.emitEvent(ctx, peering.ID, model.EventPeeringCreated, "Peering request created")

	// For same-account, simulate immediate provisioning → active
	if sameAccount {
		h.provisionPeering(ctx, peering, requesterVPC, accepterVPC)
	}

	writeCreated(w, peeringToResponse(peering))
}

func (h *PeeringHandler) provisionPeering(ctx context.Context, peering *model.Peering, requesterVPC, accepterVPC *model.VPC) {
	// Install routes for each VPC's CIDRs into the peer's route table
	for _, cidr := range accepterVPC.CIDRBlocks {
		h.routes.Upsert(ctx, &model.RouteEntry{
			Prefix:      cidr,
			OriginVPCID: accepterVPC.ID,
			PeeringID:   peering.ID,
			Origin:      model.RouteOriginDirect,
			Preference:  model.PreferenceForOrigin(model.RouteOriginDirect),
			State:       model.RouteStateActive,
		})
	}
	if peering.Direction == model.PeeringDirectionBidirectional || peering.Direction == model.PeeringDirectionAccepterToRequester {
		for _, cidr := range requesterVPC.CIDRBlocks {
			h.routes.Upsert(ctx, &model.RouteEntry{
				Prefix:      cidr,
				OriginVPCID: requesterVPC.ID,
				PeeringID:   peering.ID,
				Origin:      model.RouteOriginDirect,
				Preference:  model.PreferenceForOrigin(model.RouteOriginDirect),
				State:       model.RouteStateActive,
			})
		}
	}

	now := time.Now()
	peering.State = model.PeeringStateActive
	peering.ProvisionedAt = &now
	routes, _ := h.routes.ListByPeering(ctx, peering.ID)
	peering.RouteCount = len(routes)
	h.peerings.Update(ctx, peering)
	h.emitEvent(ctx, peering.ID, model.EventPeeringProvisioned, fmt.Sprintf("Peering is now active. %d routes installed.", len(routes)))
}

func (h *PeeringHandler) Get(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	requesterVPC, _ := h.vpcs.Get(ctx, peering.RequesterVPCID)
	accepterVPC, _ := h.vpcs.Get(ctx, peering.AccepterVPCID)

	reqAcctID := uuid.Nil
	accAcctID := uuid.Nil
	if requesterVPC != nil {
		reqAcctID = requesterVPC.AccountID
	}
	if accepterVPC != nil {
		accAcctID = accepterVPC.AccountID
	}

	if err := auth.RequirePeeringAccess(ctx, peering, reqAcctID, accAcctID); err != nil {
		writeError(w, err)
		return
	}

	routes, _ := h.routes.ListByPeering(ctx, peeringID)
	peering.RouteCount = len(routes)

	writeSuccess(w, peeringToResponse(peering))
}

func (h *PeeringHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	var vpcID *uuid.UUID
	if s := r.URL.Query().Get("vpc_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			writeError(w, model.ErrInvalidInput("invalid vpc_id"))
			return
		}
		vpcID = &id
	}

	var state *model.PeeringState
	if s := r.URL.Query().Get("state"); s != "" {
		st := model.PeeringState(s)
		state = &st
	}

	result, err := h.peerings.List(ctx, accountID, vpcID, state, store.ListParams{PageSize: 50})
	if err != nil {
		writeError(w, err)
		return
	}

	items := make([]*PeeringResponse, len(result.Items))
	for i := range result.Items {
		items[i] = peeringToResponse(&result.Items[i])
	}
	writeList(w, items, result.TotalCount, result.NextPageToken)
}

func (h *PeeringHandler) Update(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	// Only requester can update
	requesterVPC, _ := h.vpcs.Get(ctx, peering.RequesterVPCID)
	if requesterVPC == nil || requesterVPC.AccountID != accountID {
		writeError(w, model.ErrPermissionDenied("only requester can update peering"))
		return
	}

	if peering.State != model.PeeringStateActive {
		writeError(w, model.ErrInvalidState("peering must be active to update"))
		return
	}

	var req UpdatePeeringRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, model.ErrInvalidInput("invalid JSON body"))
		return
	}

	if req.Direction != nil {
		peering.Direction = model.PeeringDirection(*req.Direction)
	}
	if req.RoutePolicy != nil {
		policy, err := parseRoutePolicy(req.RoutePolicy)
		if err != nil {
			writeError(w, err)
			return
		}
		peering.RoutePolicy = policy
	}

	if err := h.peerings.Update(ctx, peering); err != nil {
		writeError(w, err)
		return
	}

	h.emitEvent(ctx, peeringID, model.EventPolicyUpdated, "Route policy updated")
	writeSuccess(w, peeringToResponse(peering))
}

func (h *PeeringHandler) Delete(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	requesterVPC, _ := h.vpcs.Get(ctx, peering.RequesterVPCID)
	accepterVPC, _ := h.vpcs.Get(ctx, peering.AccepterVPCID)

	reqAcctID := uuid.Nil
	accAcctID := uuid.Nil
	if requesterVPC != nil {
		reqAcctID = requesterVPC.AccountID
	}
	if accepterVPC != nil {
		accAcctID = accepterVPC.AccountID
	}

	if err := auth.RequirePeeringAccess(ctx, peering, reqAcctID, accAcctID); err != nil {
		writeError(w, err)
		return
	}

	if err := h.peerings.Delete(ctx, peeringID); err != nil {
		writeError(w, err)
		return
	}

	h.emitEvent(ctx, peeringID, model.EventPeeringDeleted, "Peering deleted")
	writeNoContent(w)
}

func (h *PeeringHandler) Accept(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	if peering.State != model.PeeringStatePendingAcceptance {
		writeError(w, model.ErrInvalidState("peering is not pending acceptance"))
		return
	}

	accepterVPC, _ := h.vpcs.Get(ctx, peering.AccepterVPCID)
	if accepterVPC == nil || accepterVPC.AccountID != accountID {
		writeError(w, model.ErrPermissionDenied("caller does not own accepter VPC"))
		return
	}

	peering.State = model.PeeringStateProvisioning
	h.peerings.Update(ctx, peering)
	h.emitEvent(ctx, peeringID, model.EventPeeringAccepted, "Peering accepted")

	requesterVPC, _ := h.vpcs.Get(ctx, peering.RequesterVPCID)
	if requesterVPC != nil {
		h.provisionPeering(ctx, peering, requesterVPC, accepterVPC)
	}

	writeSuccess(w, peeringToResponse(peering))
}

func (h *PeeringHandler) Reject(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	if peering.State != model.PeeringStatePendingAcceptance {
		writeError(w, model.ErrInvalidState("peering is not pending acceptance"))
		return
	}

	accepterVPC, _ := h.vpcs.Get(ctx, peering.AccepterVPCID)
	if accepterVPC == nil || accepterVPC.AccountID != accountID {
		writeError(w, model.ErrPermissionDenied("caller does not own accepter VPC"))
		return
	}

	peering.State = model.PeeringStateRejected
	h.peerings.Update(ctx, peering)

	writeSuccess(w, peeringToResponse(peering))
}

func (h *PeeringHandler) ListRoutes(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	routes, err := h.routes.ListByPeering(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	items := make([]RouteEntryResponse, len(routes))
	for i, re := range routes {
		items[i] = RouteEntryResponse{
			Prefix:      re.Prefix.String(),
			OriginVPCID: re.OriginVPCID.String(),
			State:       string(re.State),
			Origin:      string(re.Origin),
			NextHop:     re.NextHop.String(),
			Preference:  re.Preference,
		}
	}
	writeList(w, items, len(items), "")
}

func (h *PeeringHandler) OverrideRoute(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	var req OverrideRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, model.ErrInvalidInput("invalid JSON body"))
		return
	}

	prefix, err := netip.ParsePrefix(req.Prefix)
	if err != nil {
		writeError(w, model.ErrInvalidCIDR(fmt.Sprintf("invalid prefix: %s", req.Prefix)))
		return
	}

	peering, err := h.peerings.Get(ctx, peeringID)
	if err != nil {
		writeError(w, err)
		return
	}

	switch req.Action {
	case "add_static":
		route := &model.RouteEntry{
			Prefix:     prefix,
			PeeringID:  peeringID,
			Origin:     model.RouteOriginStatic,
			Preference: model.PreferenceForOrigin(model.RouteOriginStatic),
			State:      model.RouteStateActive,
		}
		if err := h.routes.Upsert(ctx, route); err != nil {
			writeError(w, err)
			return
		}
		h.emitEvent(ctx, peeringID, model.EventRouteAdded, fmt.Sprintf("Static route added: %s", prefix))
	case "withdraw":
		if err := h.routes.Delete(ctx, peeringID, prefix); err != nil {
			writeError(w, err)
			return
		}
		h.emitEvent(ctx, peeringID, model.EventRouteWithdrawn, fmt.Sprintf("Route withdrawn: %s", prefix))
	default:
		writeError(w, model.ErrInvalidInput("action must be 'add_static' or 'withdraw'"))
		return
	}

	_ = peering // suppress unused
	writeSuccess(w, map[string]string{"status": "ok"})
}

func (h *PeeringHandler) ListEvents(w http.ResponseWriter, r *http.Request, peeringID uuid.UUID) {
	ctx := r.Context()
	result, err := h.events.List(ctx, peeringID, store.ListParams{PageSize: 50})
	if err != nil {
		writeError(w, err)
		return
	}

	items := make([]EventResponse, len(result.Items))
	for i, e := range result.Items {
		items[i] = EventResponse{
			ID:        e.ID.String(),
			PeeringID: e.PeeringID.String(),
			Type:      string(e.Type),
			Message:   e.Message,
			Timestamp: e.Timestamp.Format(time.RFC3339),
		}
	}
	writeList(w, items, result.TotalCount, result.NextPageToken)
}

// GetEffectiveRoutes returns the consolidated route table for a VPC.
func (h *PeeringHandler) GetEffectiveRoutes(w http.ResponseWriter, r *http.Request, vpcID uuid.UUID) {
	ctx := r.Context()
	vpc, err := h.vpcs.Get(ctx, vpcID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := auth.RequireVPCOwner(ctx, vpc); err != nil {
		writeError(w, err)
		return
	}

	// Gather routes from all peerings involving this VPC
	accountID := vpc.AccountID
	result, _ := h.peerings.List(ctx, accountID, &vpcID, nil, store.ListParams{PageSize: 200})

	var allRoutes []RouteEntryResponse
	for _, p := range result.Items {
		if p.State != model.PeeringStateActive {
			continue
		}
		routes, _ := h.routes.ListByPeering(ctx, p.ID)
		for _, re := range routes {
			// Apply route policy evaluation
			decision, reason := model.EvaluatePrefix(p.RoutePolicy, re.Prefix, len(allRoutes))
			state := string(re.State)
			if decision == model.PolicyFiltered {
				state = string(model.RouteStateFiltered)
			}
			if decision == model.PolicyDeny {
				continue // denied routes are not shown
			}
			allRoutes = append(allRoutes, RouteEntryResponse{
				Prefix:      re.Prefix.String(),
				OriginVPCID: re.OriginVPCID.String(),
				State:       state,
				Origin:      string(re.Origin),
				NextHop:     re.NextHop.String(),
				Preference:  re.Preference,
			})
			_ = reason
		}
	}

	writeList(w, allRoutes, len(allRoutes), "")
}

func (h *PeeringHandler) emitEvent(ctx context.Context, peeringID uuid.UUID, eventType model.EventType, message string) {
	h.events.Append(ctx, &model.PeeringEvent{
		ID:        uuid.New(),
		PeeringID: peeringID,
		Type:      eventType,
		Message:   message,
		Timestamp: time.Now(),
	})
}
