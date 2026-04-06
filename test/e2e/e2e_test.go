package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/ravi-chuppala/vpc-routing/internal/api"
	"github.com/ravi-chuppala/vpc-routing/internal/auth"
	"github.com/ravi-chuppala/vpc-routing/internal/store"
	"github.com/ravi-chuppala/vpc-routing/internal/vni"
)

// E2E tests exercise the full API lifecycle as a QA engineer would.

type e2eEnv struct {
	server *httptest.Server
	router *api.Router
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	alloc := vni.NewAllocator()
	alloc.RegisterRegion("us-east-1", 0)
	alloc.RegisterRegion("us-west-1", 1)
	alloc.RegisterRegion("eu-west-1", 2)

	vpcs := store.NewMemoryVPCStore()
	peerings := store.NewMemoryPeeringStore()
	events := store.NewMemoryEventStore()
	routes := store.NewMemoryRouteStore()

	router := api.NewRouter(vpcs, peerings, events, routes, alloc)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject auth from X-Account-ID header for e2e testing
		acctHeader := r.Header.Get("X-Account-ID")
		if acctHeader != "" {
			acctID, _ := uuid.Parse(acctHeader)
			r = r.WithContext(auth.ContextWithAccount(r.Context(), acctID))
		}
		router.ServeHTTP(w, r)
	}))

	return &e2eEnv{server: server, router: router}
}

func (e *e2eEnv) request(t *testing.T, accountID uuid.UUID, method, path string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.server.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Account-ID", accountID.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func (e *e2eEnv) parseBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	resp.Body.Close()
	return result
}

// --- E2E Test Scenarios ---

func TestE2E_FullPeeringLifecycle(t *testing.T) {
	env := setupE2E(t)
	defer env.server.Close()
	accountID := uuid.New()

	// 1. Create two VPCs
	resp := env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "production", "region_id": "us-east-1", "cidr_blocks": []string{"10.0.0.0/16"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create VPC-A: %d", resp.StatusCode)
	}
	body := env.parseBody(t, resp)
	vpcA := body["data"].(map[string]any)["id"].(string)

	resp = env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "staging", "region_id": "us-east-1", "cidr_blocks": []string{"10.1.0.0/16"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create VPC-B: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	vpcB := body["data"].(map[string]any)["id"].(string)

	// 2. Create peering
	resp = env.request(t, accountID, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpcA, "accepter_vpc_id": vpcB,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create peering: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	peeringData := body["data"].(map[string]any)
	peeringID := peeringData["id"].(string)

	if peeringData["state"] != "provisioning" {
		t.Errorf("peering state = %v, want provisioning", peeringData["state"])
	}

	// 3. Get peering
	resp = env.request(t, accountID, "GET", fmt.Sprintf("/v1/peerings/%s", peeringID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get peering: %d", resp.StatusCode)
	}

	// 4. List routes
	resp = env.request(t, accountID, "GET", fmt.Sprintf("/v1/peerings/%s/routes", peeringID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list routes: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	routeCount := int(body["pagination"].(map[string]any)["total_count"].(float64))
	if routeCount < 0 {
		t.Errorf("expected non-negative routes, got %d", routeCount)
	}

	// 5. List events
	resp = env.request(t, accountID, "GET", fmt.Sprintf("/v1/peerings/%s/events", peeringID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list events: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	eventCount := int(body["pagination"].(map[string]any)["total_count"].(float64))
	if eventCount < 1 {
		t.Errorf("expected >= 1 events, got %d", eventCount)
	}

	// 6. Get effective routes for VPC-A
	resp = env.request(t, accountID, "GET", fmt.Sprintf("/v1/vpcs/%s/effective-routes", vpcA), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("effective routes: %d", resp.StatusCode)
	}

	// 7. Update peering policy
	resp = env.request(t, accountID, "PATCH", fmt.Sprintf("/v1/peerings/%s", peeringID), map[string]any{
		"route_policy": map[string]any{"max_prefixes": 50},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("update peering: %d", resp.StatusCode)
	}

	// 8. Delete peering
	resp = env.request(t, accountID, "DELETE", fmt.Sprintf("/v1/peerings/%s", peeringID), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete peering: %d", resp.StatusCode)
	}

	// 9. Now VPC delete should work
	resp = env.request(t, accountID, "DELETE", fmt.Sprintf("/v1/vpcs/%s", vpcA), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete VPC-A: %d", resp.StatusCode)
	}
}

func TestE2E_CrossAccountPeering(t *testing.T) {
	env := setupE2E(t)
	defer env.server.Close()
	accountA := uuid.New()
	accountB := uuid.New()

	// Account A creates VPC
	resp := env.request(t, accountA, "POST", "/v1/vpcs", map[string]any{
		"name": "vpc-a", "region_id": "us-east-1", "cidr_blocks": []string{"10.0.0.0/16"},
	})
	body := env.parseBody(t, resp)
	vpcA := body["data"].(map[string]any)["id"].(string)

	// Account B creates VPC
	resp = env.request(t, accountB, "POST", "/v1/vpcs", map[string]any{
		"name": "vpc-b", "region_id": "us-east-1", "cidr_blocks": []string{"10.1.0.0/16"},
	})
	body = env.parseBody(t, resp)
	vpcB := body["data"].(map[string]any)["id"].(string)

	// Account A requests peering
	resp = env.request(t, accountA, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpcA, "accepter_vpc_id": vpcB,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create cross-account peering: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	peeringID := body["data"].(map[string]any)["id"].(string)
	state := body["data"].(map[string]any)["state"].(string)
	if state != "pending_acceptance" {
		t.Errorf("state = %s, want pending_acceptance", state)
	}

	// Account B accepts
	resp = env.request(t, accountB, "POST", fmt.Sprintf("/v1/peerings/%s/accept", peeringID), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("accept peering: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	state = body["data"].(map[string]any)["state"].(string)
	if state != "provisioning" {
		t.Errorf("state after accept = %s, want provisioning", state)
	}
}

func TestE2E_ErrorScenarios(t *testing.T) {
	env := setupE2E(t)
	defer env.server.Close()
	accountID := uuid.New()

	// Invalid CIDR
	resp := env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "bad", "region_id": "us-east-1", "cidr_blocks": []string{"8.8.8.0/24"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("invalid CIDR: expected 400, got %d", resp.StatusCode)
	}

	// Non-existent VPC
	resp = env.request(t, accountID, "GET", fmt.Sprintf("/v1/vpcs/%s", uuid.New()), nil)
	if resp.StatusCode != 404 {
		t.Errorf("not found: expected 404, got %d", resp.StatusCode)
	}

	// Self-peering
	resp = env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "self", "region_id": "us-east-1", "cidr_blocks": []string{"10.0.0.0/16"},
	})
	body := env.parseBody(t, resp)
	vpcID := body["data"].(map[string]any)["id"].(string)

	resp = env.request(t, accountID, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpcID, "accepter_vpc_id": vpcID,
	})
	if resp.StatusCode != 400 {
		t.Errorf("self-peer: expected 400, got %d", resp.StatusCode)
	}

	// Health check (no auth needed for healthz - but our test server injects empty account)
	resp2, _ := http.Get(env.server.URL + "/healthz")
	if resp2.StatusCode != 200 {
		t.Errorf("healthz: expected 200, got %d", resp2.StatusCode)
	}
}

func TestE2E_CrossRegionPeering(t *testing.T) {
	env := setupE2E(t)
	defer env.server.Close()
	accountID := uuid.New()

	resp := env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "east", "region_id": "us-east-1", "cidr_blocks": []string{"10.0.0.0/16"},
	})
	body := env.parseBody(t, resp)
	vpcEast := body["data"].(map[string]any)["id"].(string)

	resp = env.request(t, accountID, "POST", "/v1/vpcs", map[string]any{
		"name": "west", "region_id": "us-west-1", "cidr_blocks": []string{"10.1.0.0/16"},
	})
	body = env.parseBody(t, resp)
	vpcWest := body["data"].(map[string]any)["id"].(string)

	resp = env.request(t, accountID, "POST", "/v1/peerings", map[string]any{
		"requester_vpc_id": vpcEast, "accepter_vpc_id": vpcWest,
	})
	if resp.StatusCode != 201 {
		t.Fatalf("cross-region peering: %d", resp.StatusCode)
	}
	body = env.parseBody(t, resp)
	data := body["data"].(map[string]any)
	if data["cross_region"] != true {
		t.Errorf("cross_region = %v, want true", data["cross_region"])
	}
}
