package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

type testEnv struct {
	router    *Router
	accountID uuid.UUID
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	alloc := vni.NewAllocator()
	alloc.RegisterRegion("us-east-1", 0)
	alloc.RegisterRegion("us-west-1", 1)
	alloc.RegisterRegion("eu-west-1", 2)

	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	return &testEnv{
		router:    NewRouter(vpcs, peerings, events, routes, alloc),
		accountID: uuid.New(),
	}
}

func (e *testEnv) doRequest(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return e.doRequestAs(t, e.accountID, method, path, body)
}

func (e *testEnv) doRequestAs(t *testing.T, accountID uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.ContextWithAccount(req.Context(), accountID))
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e *testEnv) createVPC(t *testing.T, name, region, cidr string) string {
	t.Helper()
	body := map[string]any{
		"name":        name,
		"region_id":   region,
		"cidr_blocks": []string{cidr},
	}
	w := e.doRequest(t, "POST", "/v1/vpcs", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("createVPC: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	return data["id"].(string)
}

// --- VPC Tests ---

func TestCreateVPC_Success(t *testing.T) {
	env := newTestEnv(t)
	w := env.doRequest(t, "POST", "/v1/vpcs", map[string]any{
		"name":        "test-vpc",
		"region_id":   "us-east-1",
		"cidr_blocks": []string{"10.0.0.0/16"},
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)

	if data["name"] != "test-vpc" {
		t.Errorf("name = %v, want test-vpc", data["name"])
	}
	if data["state"] != "active" {
		t.Errorf("state = %v, want active", data["state"])
	}
	if data["vni"].(float64) < 4096 {
		t.Errorf("VNI %v is in reserved range", data["vni"])
	}
}

func TestCreateVPC_InvalidCIDR(t *testing.T) {
	env := newTestEnv(t)
	w := env.doRequest(t, "POST", "/v1/vpcs", map[string]any{
		"name":        "bad-vpc",
		"region_id":   "us-east-1",
		"cidr_blocks": []string{"8.8.8.0/24"},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateVPC_CIDROverlap(t *testing.T) {
	env := newTestEnv(t)
	env.createVPC(t, "vpc-1", "us-east-1", "10.0.0.0/16")

	w := env.doRequest(t, "POST", "/v1/vpcs", map[string]any{
		"name":        "vpc-2",
		"region_id":   "us-east-1",
		"cidr_blocks": []string{"10.0.1.0/24"},
	})

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateVPC_DuplicateName(t *testing.T) {
	env := newTestEnv(t)
	env.createVPC(t, "same-name", "us-east-1", "10.0.0.0/16")

	w := env.doRequest(t, "POST", "/v1/vpcs", map[string]any{
		"name":        "same-name",
		"region_id":   "us-east-1",
		"cidr_blocks": []string{"10.1.0.0/16"},
	})

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetVPC(t *testing.T) {
	env := newTestEnv(t)
	id := env.createVPC(t, "my-vpc", "us-east-1", "10.0.0.0/16")

	w := env.doRequest(t, "GET", fmt.Sprintf("/v1/vpcs/%s", id), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetVPC_NotFound(t *testing.T) {
	env := newTestEnv(t)
	w := env.doRequest(t, "GET", fmt.Sprintf("/v1/vpcs/%s", uuid.New()), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetVPC_WrongAccount(t *testing.T) {
	env := newTestEnv(t)
	id := env.createVPC(t, "my-vpc", "us-east-1", "10.0.0.0/16")

	otherAccount := uuid.New()
	w := env.doRequestAs(t, otherAccount, "GET", fmt.Sprintf("/v1/vpcs/%s", id), nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListVPCs(t *testing.T) {
	env := newTestEnv(t)
	env.createVPC(t, "vpc-1", "us-east-1", "10.0.0.0/16")
	env.createVPC(t, "vpc-2", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "GET", "/v1/vpcs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ListResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Pagination.TotalCount != 2 {
		t.Errorf("total = %d, want 2", resp.Pagination.TotalCount)
	}
}

func TestDeleteVPC(t *testing.T) {
	env := newTestEnv(t)
	id := env.createVPC(t, "del-vpc", "us-east-1", "10.0.0.0/16")

	w := env.doRequest(t, "DELETE", fmt.Sprintf("/v1/vpcs/%s", id), nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify deleted
	w = env.doRequest(t, "GET", fmt.Sprintf("/v1/vpcs/%s", id), nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", w.Code)
	}
}

// --- Peering Tests ---

func TestCreatePeering_SameAccount(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1,
		"accepter_vpc_id":  vpc2,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)

	// Same-account → auto-accept → active
	if data["state"] != "active" {
		t.Errorf("state = %v, want active (same-account auto-accept)", data["state"])
	}
	if data["direction"] != "bidirectional" {
		t.Errorf("direction = %v, want bidirectional", data["direction"])
	}
}

func TestCreatePeering_CrossAccount(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")

	// Create VPC in different account
	otherAccount := uuid.New()
	w := env.doRequestAs(t, otherAccount, "POST", "/v1/vpcs", map[string]any{
		"name":        "vpc-b",
		"region_id":   "us-east-1",
		"cidr_blocks": []string{"10.1.0.0/16"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create accepter VPC: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	vpc2 := resp.Data.(map[string]any)["id"].(string)

	// Create cross-account peering
	w = env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1,
		"accepter_vpc_id":  vpc2,
	})

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)

	if data["state"] != "pending_acceptance" {
		t.Errorf("state = %v, want pending_acceptance", data["state"])
	}

	peeringID := data["id"].(string)

	// Accept from other account
	w = env.doRequestAs(t, otherAccount, "POST", fmt.Sprintf("/v1/peerings/%s/accept", peeringID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("accept: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	json.NewDecoder(w.Body).Decode(&resp)
	data = resp.Data.(map[string]any)
	if data["state"] != "active" {
		t.Errorf("state after accept = %v, want active", data["state"])
	}
}

func TestCreatePeering_Reject(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")

	otherAccount := uuid.New()
	w := env.doRequestAs(t, otherAccount, "POST", "/v1/vpcs", map[string]any{
		"name": "vpc-b", "region_id": "us-east-1", "cidr_blocks": []string{"10.1.0.0/16"},
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	vpc2 := resp.Data.(map[string]any)["id"].(string)

	w = env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	// Reject from other account
	w = env.doRequestAs(t, otherAccount, "POST", fmt.Sprintf("/v1/peerings/%s/reject", peeringID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	if data["state"] != "rejected" {
		t.Errorf("state = %v, want rejected", data["state"])
	}
}

func TestCreatePeering_DuplicateCheck(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})

	// Duplicate
	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreatePeering_CIDROverlap(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	// Can't create overlapping VPC in same account, so this tests same CIDR
	// Actually the CIDR overlap check for peering is between VPC-A and VPC-B CIDRs.
	// Since we validate at VPC creation that CIDRs don't overlap within an account,
	// we need different accounts for same-CIDR peering test.

	otherAccount := uuid.New()
	w := env.doRequestAs(t, otherAccount, "POST", "/v1/vpcs", map[string]any{
		"name": "vpc-overlap", "region_id": "us-east-1", "cidr_blocks": []string{"10.0.0.0/16"},
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	vpc2 := resp.Data.(map[string]any)["id"].(string)

	w = env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for CIDR overlap, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreatePeering_SelfPeer(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc1,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeletePeering(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	w = env.doRequest(t, "DELETE", fmt.Sprintf("/v1/peerings/%s", peeringID), nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteVPC_WithActivePeerings(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})

	w := env.doRequest(t, "DELETE", fmt.Sprintf("/v1/vpcs/%s", vpc1), nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (has active peerings), got %d: %s", w.Code, w.Body.String())
	}
}

// --- Route and Event Tests ---

func TestListRoutes(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	w = env.doRequest(t, "GET", fmt.Sprintf("/v1/peerings/%s/routes", peeringID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp ListResponse
	json.NewDecoder(w.Body).Decode(&listResp)
	// Bidirectional peering → routes from both VPCs
	if listResp.Pagination.TotalCount < 2 {
		t.Errorf("expected at least 2 routes, got %d", listResp.Pagination.TotalCount)
	}
}

func TestListEvents(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	w = env.doRequest(t, "GET", fmt.Sprintf("/v1/peerings/%s/events", peeringID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp ListResponse
	json.NewDecoder(w.Body).Decode(&listResp)
	// Should have peering_created + peering_provisioned events
	if listResp.Pagination.TotalCount < 2 {
		t.Errorf("expected at least 2 events, got %d", listResp.Pagination.TotalCount)
	}
}

func TestEffectiveRoutes(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")
	vpc3 := env.createVPC(t, "vpc-c", "us-east-1", "10.2.0.0/16")

	// Create two peerings for vpc1
	env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc3,
	})

	w := env.doRequest(t, "GET", fmt.Sprintf("/v1/vpcs/%s/effective-routes", vpc1), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var listResp ListResponse
	json.NewDecoder(w.Body).Decode(&listResp)
	// Should see routes from both peerings
	if listResp.Pagination.TotalCount < 2 {
		t.Errorf("expected at least 2 effective routes, got %d", listResp.Pagination.TotalCount)
	}
}

func TestUpdatePeering_PolicyChange(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	// Update route policy
	maxPfx := 50
	w = env.doRequest(t, "PATCH", fmt.Sprintf("/v1/peerings/%s", peeringID), map[string]any{
		"route_policy": map[string]any{
			"max_prefixes":    maxPfx,
			"denied_prefixes": []string{"10.1.255.0/24"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	json.NewDecoder(w.Body).Decode(&resp)
	data := resp.Data.(map[string]any)
	policy := data["route_policy"].(map[string]any)
	if int(policy["max_prefixes"].(float64)) != 50 {
		t.Errorf("max_prefixes = %v, want 50", policy["max_prefixes"])
	}
}

func TestOverrideRoute(t *testing.T) {
	env := newTestEnv(t)
	vpc1 := env.createVPC(t, "vpc-a", "us-east-1", "10.0.0.0/16")
	vpc2 := env.createVPC(t, "vpc-b", "us-east-1", "10.1.0.0/16")

	w := env.doRequest(t, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpc1, "accepter_vpc_id": vpc2,
	})
	var resp SuccessResponse
	json.NewDecoder(w.Body).Decode(&resp)
	peeringID := resp.Data.(map[string]any)["id"].(string)

	// Add static route
	w = env.doRequest(t, "POST", fmt.Sprintf("/v1/peerings/%s/routes", peeringID), map[string]any{
		"action": "add_static",
		"prefix": "10.1.1.0/24",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("add_static: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Withdraw route
	w = env.doRequest(t, "POST", fmt.Sprintf("/v1/peerings/%s/routes", peeringID), map[string]any{
		"action": "withdraw",
		"prefix": "10.1.1.0/24",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("withdraw: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHealthEndpoint(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
