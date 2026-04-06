package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

type VPCHandler struct {
	vpcs     store.VPCStore
	peerings store.PeeringStore
	alloc    *vni.Allocator
}

func NewVPCHandler(vpcs store.VPCStore, peerings store.PeeringStore, alloc *vni.Allocator) *VPCHandler {
	return &VPCHandler{vpcs: vpcs, peerings: peerings, alloc: alloc}
}

type CreateVPCRequest struct {
	Name       string   `json:"name"`
	RegionID   string   `json:"region_id"`
	CIDRBlocks []string `json:"cidr_blocks"`
}

type VPCResponse struct {
	ID           string   `json:"id"`
	AccountID    string   `json:"account_id"`
	RegionID     string   `json:"region_id"`
	Name         string   `json:"name"`
	CIDRBlocks   []string `json:"cidr_blocks"`
	VNI          uint32   `json:"vni"`
	VRFName      string   `json:"vrf_name"`
	State        string   `json:"state"`
	PeeringCount int      `json:"peering_count"`
	CreatedAt    string   `json:"created_at"`
}

func vpcToResponse(v *model.VPC) *VPCResponse {
	cidrs := make([]string, len(v.CIDRBlocks))
	for i, c := range v.CIDRBlocks {
		cidrs[i] = c.String()
	}
	return &VPCResponse{
		ID:           v.ID.String(),
		AccountID:    v.AccountID.String(),
		RegionID:     v.RegionID,
		Name:         v.Name,
		CIDRBlocks:   cidrs,
		VNI:          v.VNI,
		VRFName:      v.VRFName,
		State:        string(v.State),
		PeeringCount: v.PeeringCount,
		CreatedAt:    v.CreatedAt.Format(time.RFC3339),
	}
}

func (h *VPCHandler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	var req CreateVPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, model.ErrInvalidInput("invalid JSON body"))
		return
	}

	// Validate name
	if req.Name == "" || len(req.Name) > 128 {
		writeError(w, model.ErrInvalidInput("name must be 1-128 characters"))
		return
	}
	if req.RegionID == "" {
		writeError(w, model.ErrInvalidInput("region_id is required"))
		return
	}

	// Parse and validate CIDRs
	cidrs := make([]netip.Prefix, len(req.CIDRBlocks))
	for i, s := range req.CIDRBlocks {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			writeError(w, model.ErrInvalidCIDR(fmt.Sprintf("invalid CIDR: %s", s)))
			return
		}
		cidrs[i] = p
	}
	normalizedCIDRs, err := model.ValidateCIDRBlocks(cidrs)
	if err != nil {
		writeError(w, model.ErrInvalidCIDR(err.Error()))
		return
	}
	cidrs = normalizedCIDRs

	// Check name uniqueness
	existing, err := h.vpcs.FindByName(ctx, accountID, req.RegionID, req.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	if existing != nil {
		writeError(w, model.ErrConflict("VPC name already exists in this account+region"))
		return
	}

	// Check CIDR overlap
	overlap, err := h.vpcs.FindOverlappingCIDR(ctx, accountID, cidrs)
	if err != nil {
		writeError(w, err)
		return
	}
	if overlap != nil {
		writeError(w, model.ErrCIDROverlap(fmt.Sprintf("CIDR overlaps with VPC %s", overlap.ID)))
		return
	}

	// Allocate VNI
	allocatedVNI, err := h.alloc.Allocate(ctx, req.RegionID, accountID)
	if err != nil {
		writeError(w, model.ErrVNIExhausted())
		return
	}

	id := uuid.New()
	vpc := &model.VPC{
		ID:         id,
		AccountID:  accountID,
		RegionID:   req.RegionID,
		Name:       req.Name,
		CIDRBlocks: cidrs,
		VNI:        allocatedVNI,
		VRFName:    fmt.Sprintf("vpc-%s", id.String()[:8]),
		RD:         fmt.Sprintf("%s:%s", req.RegionID, id.String()[:8]),
		ExportRT:   fmt.Sprintf("target:%s:%s", accountID.String()[:8], id.String()[:8]),
		State:      model.VPCStateActive,
		CreatedAt:  time.Now(),
	}

	if err := h.vpcs.Create(ctx, vpc); err != nil {
		writeError(w, err)
		return
	}

	writeCreated(w, vpcToResponse(vpc))
}

func (h *VPCHandler) Get(w http.ResponseWriter, r *http.Request, vpcID uuid.UUID) {
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

	count, _ := h.peerings.CountByVPC(ctx, vpcID)
	vpc.PeeringCount = count

	writeSuccess(w, vpcToResponse(vpc))
}

func (h *VPCHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accountID, err := auth.AccountFromContext(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	regionID := r.URL.Query().Get("region_id")
	result, err := h.vpcs.List(ctx, accountID, regionID, store.ListParams{PageSize: 50})
	if err != nil {
		writeError(w, err)
		return
	}

	items := make([]*VPCResponse, len(result.Items))
	for i := range result.Items {
		items[i] = vpcToResponse(&result.Items[i])
	}
	writeList(w, items, result.TotalCount, result.NextPageToken)
}

func (h *VPCHandler) Delete(w http.ResponseWriter, r *http.Request, vpcID uuid.UUID) {
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

	count, _ := h.peerings.CountByVPC(ctx, vpcID)
	if count > 0 {
		writeError(w, model.ErrHasActivePeerings())
		return
	}

	if err := h.vpcs.Delete(ctx, vpcID); err != nil {
		writeError(w, err)
		return
	}

	_ = h.alloc.Release(ctx, vpc.VNI)

	writeNoContent(w)
}
