# VPC Interconnect — Step-by-Step Execution Plan

**Status**: Draft  
**Date**: 2026-04-06  
**Based on**: [Functional Spec](vpc-interconnect-functional-spec.md) | [Technical Architecture](vpc-interconnect-architecture.md)

---

## Overview

This plan breaks the VPC Interconnect product into concrete, ordered execution steps — each with defined inputs, outputs, dependencies, and acceptance criteria. Steps are sequenced so that each builds on the last and produces something testable.

**Total timeline**: ~36 weeks across 3 phases  
**Team assumption**: 3–5 engineers (Go backend, networking/control-plane, infrastructure/platform)

---

## Phase 1: Single-Region Peering (Weeks 1–16)

> **Goal**: Customers can create VPC peerings within a single region via REST API. Traffic flows at line rate through the EVPN-VXLAN fabric. Sub-1s failover.

---

### Step 1.1: Repository Bootstrap and Go Project Structure (Week 1)

**What**: Initialize the Go monorepo with module structure, CI pipeline, and dev tooling.

**Tasks**:
1. `git init`, create `go.mod` (`go 1.22+`), configure `.gitignore`
2. Create directory structure:
   ```
   /
   ├── cmd/
   │   ├── api/           # vpc-interconnect-api entrypoint
   │   ├── controller/    # vpc-interconnect-controller entrypoint
   │   ├── bgp/           # vpc-interconnect-bgp entrypoint
   │   └── agent/         # vpc-interconnect-agent entrypoint
   ├── internal/
   │   ├── api/           # HTTP/gRPC handlers, middleware
   │   ├── controller/    # Peering lifecycle, reconciliation loop
   │   ├── bgp/           # GoBGP wrapper, EVPN route builder
   │   ├── agent/         # Netlink VRF/VXLAN programming
   │   ├── model/         # Domain types (VPC, Peering, RoutePolicy, etc.)
   │   ├── store/         # PostgreSQL repository layer
   │   ├── vni/           # VNI allocator
   │   └── auth/          # JWT/API-key validation, account scoping
   ├── proto/             # Protobuf definitions (gRPC services + models)
   ├── migrations/        # SQL migration files (golang-migrate)
   ├── deploy/            # Dockerfiles, systemd units, Helm charts
   ├── test/              # Integration and e2e tests
   └── docs/design/       # (already exists)
   ```
3. Add Go dependencies: `google.golang.org/grpc`, `github.com/grpc-ecosystem/grpc-gateway/v2`, `github.com/jackc/pgx/v5`, `github.com/google/uuid`, `log/slog`
4. Set up CI: lint (`golangci-lint`), `go vet`, `go test ./...`, build all binaries
5. Create `Makefile` with targets: `build`, `test`, `lint`, `proto`, `migrate-up`, `migrate-down`
6. Set up Docker Compose for local dev: PostgreSQL 16, optional Jaeger for tracing

**Acceptance**: `make build` produces 4 binaries. `make test` passes. CI runs on push.

**Dependencies**: None (first step)

---

### Step 1.2: Protobuf Definitions and Code Generation (Week 1–2)

**What**: Define all API contracts in protobuf. Generate Go code, gRPC stubs, REST gateway stubs, and OpenAPI spec.

**Tasks**:
1. Define `proto/vpc/v1/vpc.proto`:
   - `VpcService`: `CreateVpc`, `GetVpc`, `ListVpcs`, `DeleteVpc`, `GetEffectiveRoutes`
   - Message types: `Vpc`, `CreateVpcRequest`, `ListVpcsRequest`, `ListVpcsResponse`
2. Define `proto/peering/v1/peering.proto`:
   - `PeeringService`: `CreatePeering`, `GetPeering`, `ListPeerings`, `UpdatePeering`, `DeletePeering`, `AcceptPeering`, `RejectPeering`, `ListRoutes`, `OverrideRoute`, `ListEvents`
   - Message types: `Peering`, `RoutePolicy`, `RouteEntry`, `PeeringEvent`
3. Define `proto/internal/v1/controller.proto`:
   - Internal gRPC between controller ↔ BGP service and controller ↔ agent
   - `BgpControlService`: `ConfigureRT`, `InjectRoutes`, `WithdrawRoutes`
   - `AgentControlService`: `ProgramVRF`, `ProgramACL`, `ReportRouteStatus`
4. Add `google.api.http` annotations for REST gateway mapping
5. Configure `buf.yaml` or Makefile proto target with `protoc-gen-go`, `protoc-gen-go-grpc`, `protoc-gen-grpc-gateway`, `protoc-gen-openapiv2`
6. Generate code → verify compilation

**Acceptance**: `make proto` generates Go stubs. `make build` still passes. OpenAPI JSON produced at `docs/api/openapi.json`.

**Dependencies**: Step 1.1

---

### Step 1.3: Domain Models and Database Schema (Week 2)

**What**: Implement Go domain types and PostgreSQL schema. Set up migrations.

**Tasks**:
1. Implement `internal/model/` types matching the functional spec:
   - `VPC` struct with `uuid.UUID`, `netip.Prefix` CIDRs, `VPCState` enum
   - `Peering` struct with `PeeringState` enum, `PeeringDirection` enum
   - `RoutePolicy` struct with `AllowedPrefixes`, `DeniedPrefixes`, `MaxPrefixes`, `BandwidthLimitMbps`
   - `RouteEntry` struct with `RouteOrigin`, `RouteState` enums
   - `PeeringEvent` struct with `EventType` enum (14 event types from FR-8.2)
2. Create SQL migration `001_initial_schema.up.sql`:
   ```sql
   CREATE TABLE vpcs (
       id UUID PRIMARY KEY,
       account_id UUID NOT NULL,
       region_id TEXT NOT NULL,
       name TEXT NOT NULL,
       cidr_blocks INET[] NOT NULL,
       vni INTEGER NOT NULL UNIQUE,
       vrf_name TEXT NOT NULL,
       rd TEXT NOT NULL,
       export_rt TEXT NOT NULL,
       state TEXT NOT NULL DEFAULT 'active',
       created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
       deleted_at TIMESTAMPTZ,
       UNIQUE(account_id, region_id, name)
   );

   CREATE TABLE peerings (...) PARTITION BY LIST (region_id);
   CREATE TABLE peering_events (...);
   CREATE TABLE route_overrides (...);
   CREATE TABLE vni_allocations (...);
   ```
3. Add composite indexes: `(account_id, vpc_id)` on peerings, `(peering_id, created_at)` on events
4. Create down migration `001_initial_schema.down.sql`
5. Implement `internal/store/` with PostgreSQL repository interfaces:
   - `VPCStore`: `Create`, `Get`, `List`, `Delete`, `FindByCIDR`
   - `PeeringStore`: `Create`, `Get`, `List`, `Update`, `Delete`, `FindByVPC`
   - `EventStore`: `Append`, `List`
   - `VNIStore`: `Allocate`, `Release`
6. Write unit tests for each store method against a test PostgreSQL (use `testcontainers-go`)

**Acceptance**: `make migrate-up` creates tables. Store tests pass. Domain types match functional spec schemas exactly.

**Dependencies**: Step 1.1

---

### Step 1.4: VNI Allocator (Week 2–3)

**What**: Implement the VNI allocation service per the data plane spec (24-bit partitioned space).

**Tasks**:
1. Implement `internal/vni/allocator.go`:
   - `Allocate(ctx, regionID, accountID) (uint32, error)` — allocates next available VNI in the region's partition
   - `Release(ctx, vni uint32) error` — soft-delete with 7-day grace period
   - Enforce reserved ranges: VNI 1–4095 and 16,000,000+ are excluded
2. Implement the bitfield encoding/decoding helpers (region 4 bits, tenant shard 10 bits, VPC sequence 10 bits) for debuggability
3. Database-backed allocation with `SELECT ... FOR UPDATE SKIP LOCKED` to handle concurrent allocation
4. Unit + integration tests: concurrent allocation does not produce duplicates, release + re-allocate after grace period

**Acceptance**: 1000 concurrent allocations produce 1000 unique VNIs. Released VNI is not reusable for 7 days. Reserved ranges are never allocated.

**Dependencies**: Step 1.3

---

### Step 1.5: Authentication and Authorization Middleware (Week 3)

**What**: Implement API auth layer per FR-10.

**Tasks**:
1. Implement `internal/auth/` package:
   - JWT validation (parse, verify signature, extract `account_id` claim)
   - API key validation (database lookup or cache)
   - `AuthMiddleware` for gRPC interceptor and REST middleware
   - `AccountFromContext(ctx) uuid.UUID` helper
2. Implement authorization checks as reusable functions:
   - `RequireVPCOwner(ctx, vpc) error` — 403 if `vpc.AccountID != AccountFromContext(ctx)`
   - `RequirePeeringAccess(ctx, peering) error` — 403 if caller owns neither VPC
3. Implement rate limiter (per-account, token bucket):
   - 60/min mutating, 300/min reads, 120/min metrics
   - Return `429` with `Retry-After` header
4. Unit tests for auth extraction, rate limiting

**Acceptance**: Unauthenticated request → 401. Wrong account → 403. Burst beyond limit → 429 with Retry-After.

**Dependencies**: Step 1.3

---

### Step 1.6: VPC API — CRUD Endpoints (Week 3–4)

**What**: Implement FR-1 (VPC Management) end-to-end.

**Tasks**:
1. Implement `internal/api/vpc_handler.go`:
   - `CreateVpc`: validate input → CIDR overlap check (query `FindByCIDR` across account) → allocate VNI → assign RD + RT → persist → return
   - `GetVpc`: auth check → fetch → return with peering count
   - `ListVpcs`: auth scoped → paginate → return
   - `DeleteVpc`: check zero peerings → set state `deleting` → release VNI → return
2. Wire handlers into gRPC server and REST gateway
3. Implement CIDR validation helpers in `internal/model/cidr.go`:
   - RFC 1918 check (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`)
   - Prefix length between /16 and /28
   - Overlap detection between two `netip.Prefix` sets
4. Implement idempotency key support (FR-8.2): store `(idempotency_key, response)` in a cache/table with 24h TTL
5. Integration tests:
   - Create VPC → verify VNI allocated, RD/RT assigned
   - Create VPC with overlapping CIDR → 409
   - Delete VPC with active peering → 409
   - List VPCs → only returns caller's VPCs
   - Idempotent create → same response on retry

**Acceptance**: All FR-1 scenarios pass. OpenAPI spec matches implementation. p99 < 200ms read, < 500ms write on local Postgres.

**Dependencies**: Steps 1.2, 1.3, 1.4, 1.5

---

### Step 1.7: Peering API — CRUD Endpoints (Week 4–5)

**What**: Implement FR-2 (Peering Lifecycle) API layer. At this stage, provisioning is a stub — state goes to `provisioning` but actual route programming comes in later steps.

**Tasks**:
1. Implement `internal/api/peering_handler.go`:
   - `CreatePeering`: ownership check → CIDR overlap check between VPCs → duplicate peering check → quota check → same-account auto-accept → persist with state `provisioning` (or `pending_acceptance` for cross-account) → emit `peering_created` event
   - `GetPeering`: auth check → fetch → compute health status → return
   - `ListPeerings`: auth scoped → filter by vpc_id/state → paginate
   - `UpdatePeering`: auth check → validate only mutable fields → persist → emit `policy_updated` event
   - `DeletePeering`: auth check → set `deleting` → emit `peering_deleted` event → return
2. Implement `AcceptPeering` and `RejectPeering` (FR-3):
   - Validate state is `pending_acceptance`
   - Validate caller owns accepter VPC
   - Accept → transition to `provisioning`, emit `peering_accepted`
   - Reject → transition to `rejected`
3. Implement pending peering expiry: background goroutine expires peerings older than 7 days
4. Implement `ListRoutes` and `OverrideRoute` (FR-4) — initially returns routes from database (populated by controller in later steps)
5. Implement `ListEvents` (FR-8.2) — query `peering_events` table, sorted by timestamp desc, paginated
6. Integration tests:
   - Same-account create → state `provisioning`
   - Cross-account create → state `pending_acceptance` → accept → `provisioning`
   - Cross-account create → reject → `rejected`
   - Duplicate peering → 409
   - Overlapping CIDRs → 409
   - Quota exceeded → 422
   - Delete from either side → allowed
   - Update immutable field → 400
   - 7-day expiry fires

**Acceptance**: All FR-2, FR-3, FR-4 API scenarios pass. State machine transitions are correct. Events emitted for every lifecycle transition.

**Dependencies**: Step 1.6

---

### Step 1.8: Effective Routes API (Week 5)

**What**: Implement FR-8.3 (`GET /v1/vpcs/{vpc_id}/effective-routes`) and FR-5 (route policy evaluation).

**Tasks**:
1. Implement route policy evaluator in `internal/model/policy.go`:
   - `EvaluatePrefix(policy RoutePolicy, prefix netip.Prefix, currentCount int) (accept bool, reason string)`
   - Evaluation order: denied → allowed → max_prefixes → accept (per FR-5.2)
2. Implement `GetEffectiveRoutes` handler:
   - Query all active peerings for the VPC
   - For each peering, list routes and apply route policy evaluation
   - Merge routes, compute preference (direct > transit > static)
   - Return consolidated view
3. Unit tests for policy evaluator edge cases:
   - Prefix in both allowed and denied → denied wins
   - Null allowed_prefixes → all accepted
   - Max-prefix boundary (exactly at limit, one over)
4. Integration test: create 2 peerings for a VPC → effective routes shows merged view

**Acceptance**: Route policy evaluation matches FR-5.2 exactly. Effective routes API returns merged view across peerings.

**Dependencies**: Step 1.7

---

### Step 1.9: GoBGP Integration — BGP Service (Week 5–7)

**What**: Implement `vpc-interconnect-bgp` — the core control plane component that injects/withdraws EVPN Type-5 routes.

**Tasks**:
1. Implement `internal/bgp/server.go`:
   - Embed `gobgp.BgpServer` (from `github.com/osrg/gobgp/v3`)
   - Configure ASN, router ID, EVPN address family
   - Peer with fabric route reflectors (configurable list of RR IPs)
   - TCP-AO authentication on BGP sessions
2. Implement `internal/bgp/evpn.go` — EVPN Type-5 route builder:
   - `BuildType5Route(vpc VPC, targetVNI uint32) *apipb.Path`
   - Set Route Distinguisher: `<region-id>:<vpc-id>`
   - Set Route Targets: export RT of the source VPC
   - Set VNI/Label field to target VPC's VNI
   - Set IP prefix from VPC CIDR blocks
3. Implement `internal/bgp/service.go` — gRPC service implementing `BgpControlService`:
   - `ConfigureRT(req)`: add/remove import RT on a VRF. When called for a new peering, adds accepter's export RT to requester's import RT list (and vice versa)
   - `InjectRoutes(req)`: call `gobgp.AddPath()` for each VPC CIDR as a Type-5 route
   - `WithdrawRoutes(req)`: call `gobgp.DeletePath()` to withdraw routes
   - `GetRouteTable(req)`: call `gobgp.ListPath()` to return current RIB for a VRF
4. Implement BGP session monitoring: watch `gobgp.MonitorPeerState()`, log session up/down, expose as metric
5. Unit tests with mock GoBGP server (or in-process server without real peers):
   - Type-5 route construction produces correct NLRI
   - RT configuration adds/removes correctly
   - Route injection → appears in local RIB
   - Route withdrawal → removed from local RIB
6. Integration test (requires test fabric or GoBGP-to-GoBGP peering):
   - Two GoBGP instances peering as RR-client + RR
   - Inject route on one → appears on the other via RR

**Acceptance**: Type-5 routes constructed per RFC 9136. Routes appear in peer's RIB. BGP sessions authenticated with TCP-AO.

**Dependencies**: Step 1.2 (proto definitions for internal gRPC)

---

### Step 1.10: Host Agent — VRF and VXLAN Programming (Week 7–9)

**What**: Implement `vpc-interconnect-agent` — the per-host daemon that programs Linux VRFs, VXLAN interfaces, and nftables rules.

**Tasks**:
1. Implement `internal/agent/netlink.go` using `github.com/vishvananda/netlink`:
   - `CreateVRF(name string, tableID int) error` — create Linux VRF device
   - `DeleteVRF(name string) error`
   - `CreateVXLANInterface(vni uint32, vtepIP netip.Addr) error` — create VXLAN device bound to VRF
   - `AddRoute(vrf string, prefix netip.Prefix, nextHop netip.Addr, vni uint32) error`
   - `DeleteRoute(vrf string, prefix netip.Prefix) error`
   - `ListRoutes(vrf string) ([]RouteEntry, error)`
2. Implement `internal/agent/acl.go` — nftables/iptables rule programming:
   - `ProgramACL(vrf string, rules []ACLRule) error` — translate RoutePolicy into nftables rules
   - `ClearACL(vrf string) error`
   - 5-tuple match: src prefix, dst prefix, protocol, src port range, dst port range
3. Implement `internal/agent/service.go` — gRPC service implementing `AgentControlService`:
   - `ProgramVRF(req)` — create/delete VRF + VXLAN interface
   - `ProgramACL(req)` — install/update ACL rules for a peering
   - `ReportRouteStatus(req)` — read current VRF routes and report back to controller
4. Implement reconciliation loop:
   - Every 30 seconds, compare desired state (from controller) with actual kernel state (from netlink)
   - Fix any drift (missing VRFs, stale routes, incorrect ACLs)
   - Report drift events to controller
5. Implement systemd service file (`deploy/vpc-interconnect-agent.service`)
6. Tests (require Linux with netlink — use integration test environment or network namespaces):
   - Create VRF → `ip link show type vrf` confirms
   - Add route to VRF → `ip route show vrf <name>` confirms
   - ACL deny → packet is dropped (test with `ping` in namespace)
   - Reconciliation: manually delete a route → agent re-adds within 30s

**Acceptance**: Agent can create/delete VRFs, VXLAN interfaces, routes, and ACLs on a Linux host. Reconciliation corrects drift within 30 seconds.

**Dependencies**: Step 1.2 (proto for agent gRPC)

---

### Step 1.11: Controller — Peering Orchestration (Week 8–10)

**What**: Implement `vpc-interconnect-controller` — the brain that ties API, BGP service, and agent together.

**Tasks**:
1. Implement `internal/controller/peering.go` — peering provisioning workflow:
   - On `provisioning` state:
     1. Call BGP service `ConfigureRT` to add import/export RTs
     2. Call BGP service `InjectRoutes` for both VPCs' CIDRs
     3. Call agent `ProgramVRF` on relevant hosts (hosts running VMs in both VPCs)
     4. Call agent `ProgramACL` if route policy has ACL rules
     5. Poll agent `ReportRouteStatus` until routes confirmed installed
     6. Transition peering state to `active`, emit `peering_provisioned` event
   - On `deleting` state:
     1. Call BGP service `WithdrawRoutes`
     2. Call BGP service `ConfigureRT` to remove import/export RTs
     3. Call agent to clean up VRF routes and ACLs
     4. Transition to `deleted`
2. Implement `internal/controller/reconciler.go` — reconciliation loop:
   - Run every 10 seconds
   - Query all peerings in `provisioning` or `active` state
   - For each, verify BGP routes are present (via BGP service `GetRouteTable`)
   - For each, verify agent has correct routes installed (via agent `ReportRouteStatus`)
   - Fix any divergence: re-inject missing routes, re-program missing VRFs
   - Log and emit events for any drift detected
3. Implement leader election via PostgreSQL advisory lock:
   - `SELECT pg_try_advisory_lock(12345)` — only one controller instance runs reconciliation
   - On lock loss, stop reconciliation, log warning
   - Standby instances poll lock every 5 seconds
4. Implement provisioning timeout:
   - If peering is in `provisioning` for > 5 minutes, transition to `failed`
   - Retry failed peerings up to 10 times (every 60s), then stop
5. Implement route policy update handling:
   - On `PATCH /peerings/{id}` with changed route_policy:
     - Re-evaluate all routes against new policy
     - Withdraw newly-denied prefixes via BGP service
     - Advertise newly-allowed prefixes
     - Reprogram ACLs via agent
6. Integration tests (full stack: API → Controller → BGP → Agent):
   - Create peering → controller provisions → routes installed → state `active`
   - Delete peering → controller deprovisions → routes withdrawn → traffic stops
   - Kill controller → standby takes over → peerings remain active (graceful restart)
   - Provisioning timeout → state `failed` → auto-retry → eventually succeeds

**Acceptance**: End-to-end peering flow works. Controller handles failures gracefully. Leader election prevents dual-active. Reconciliation detects and corrects drift.

**Dependencies**: Steps 1.7, 1.9, 1.10

---

### Step 1.12: API Server Assembly and REST Gateway (Week 10–11)

**What**: Wire everything together into the `vpc-interconnect-api` binary with REST gateway.

**Tasks**:
1. Implement `cmd/api/main.go`:
   - Start gRPC server on internal port (e.g., `:9090`)
   - Start grpc-gateway REST proxy on public port (e.g., `:8080`)
   - Connect to PostgreSQL, initialize stores
   - Wire auth middleware, rate limiter, request ID injection
   - Structured logging with `slog`, request/response logging
2. Implement common response envelope (per functional spec 6.3):
   - Wrap all responses in `{"data": ..., "request_id": "..."}` for success
   - Wrap errors in `{"error": {"code": "...", "message": "..."}, "request_id": "..."}`
3. Implement OpenAPI serving: `GET /v1/docs` serves generated OpenAPI spec
4. Implement health check endpoints:
   - `GET /healthz` — basic liveness
   - `GET /readyz` — checks PostgreSQL connectivity, BGP service reachability
5. Dockerize: `deploy/Dockerfile.api`
6. End-to-end smoke test via `curl`:
   - Create VPC, create peering, get peering, list routes, delete peering, delete VPC

**Acceptance**: Full REST API functional. Health checks work. Docker container starts and serves requests.

**Dependencies**: Steps 1.6, 1.7, 1.8

---

### Step 1.13: Integration Testing and Failure Scenarios (Week 11–13)

**What**: Comprehensive end-to-end testing of all Phase 1 functional requirements.

**Tasks**:
1. **Happy path test suite** (`test/e2e/`):
   - FR-1: VPC CRUD lifecycle (create, get, list, delete)
   - FR-2: Same-account peering lifecycle (create → active → traffic flows → delete)
   - FR-3: Cross-account peering (create → pending → accept → active)
   - FR-3: Cross-account peering rejection and expiry
   - FR-4: Route listing and static route overrides
   - FR-5: Route policy — allowed/denied prefixes, max-prefix violation
   - FR-8.2: Event log — verify events emitted for every lifecycle transition
   - FR-8.3: Effective routes — merged view across multiple peerings
   - FR-10: Auth (401/403), rate limiting (429)
2. **Failure scenario test suite**:
   - BGP service crash → routes preserved (graceful restart) → service restarts → reconciles
   - Agent crash → kernel state persists → agent restarts → reconciles
   - Controller crash → standby takes over → peerings unaffected
   - PostgreSQL connection loss → API returns 503 → reconnects on recovery
   - Provisioning timeout → state `failed` → auto-retry
   - Concurrent peering creation for same VPC pair → only one succeeds (duplicate check)
3. **Performance test suite**:
   - Peering provisioning latency: target < 30 seconds
   - API response time: target p99 < 200ms reads, < 500ms writes
   - Route convergence time: measure from BGP injection to agent confirmation
4. **Security test suite**:
   - Cross-account visibility: verify requester cannot see accepter VPC details
   - CIDR validation: reject public IPs, reject < /28, reject > /16
   - Rate limiter: verify burst + sustained rate enforcement

**Acceptance**: All test suites pass. Performance targets met. No security violations.

**Dependencies**: Steps 1.11, 1.12

---

### Step 1.14: Hardening, Documentation, and Runbooks (Week 14–16)

**What**: Production readiness.

**Tasks**:
1. **Error handling hardening**:
   - Audit all error paths: ensure correct HTTP status codes per functional spec Section 8
   - Implement idempotency key storage (24h TTL) for `POST` endpoints
   - Add request validation for all input fields (max lengths, format checks, range checks)
2. **Observability baseline**:
   - OpenTelemetry SDK integration in all services
   - Structured logging with trace ID propagation
   - Basic Prometheus metrics: API latency histograms, BGP session state gauges, peering state gauges
   - Grafana dashboard template for operations
3. **Graceful restart testing**:
   - GoBGP graceful restart (RFC 4724): verify routes preserved during BGP service restart
   - Controller leader election failover: verify < 10s
   - Agent restart: verify kernel state persists, reconciliation runs
4. **Deployment artifacts**:
   - Dockerfiles for all 4 services
   - Systemd unit for agent
   - Helm chart (or equivalent) for api + controller + bgp service
   - Database migration CI job
5. **Documentation**:
   - API reference (generated from OpenAPI)
   - Operations runbook: common failure modes, troubleshooting steps, manual recovery procedures
   - Configuration reference: environment variables, config file format
6. **Load test**: 100 VPCs, 500 peerings, verify system stability

**Acceptance**: All services deployable. Graceful restart works. Load test passes at target scale. Runbooks cover all failure modes from Section 8.

**Dependencies**: Step 1.13

---

## Phase 2: Multi-Region + Observability + Metering (Weeks 17–30)

> **Goal**: Cross-region peering, customer-facing metrics, billing-grade metering, XDP fast-path ACLs.

---

### Step 2.1: Cross-Region Route Propagation (Week 17–19)

**What**: Implement FR-6 — enable peering between VPCs in different regions.

**Tasks**:
1. Extend `CreatePeering` to detect cross-region (compare VPC region IDs)
2. Set `cross_region: true` on peering response
3. Extend BGP service to attach BGP communities to cross-region routes:
   - `vpc-interconnect:<region-id>:local` for same-region
   - `vpc-interconnect:<region-id>:remote` for cross-region
4. Implement MED calculation based on inter-region latency:
   - Read latency measurements (initially static config, then dynamic in step 2.2)
   - Set MED on Type-5 routes proportional to latency
5. Extend controller to handle cross-region provisioning:
   - Routes published locally → fabric propagates via DCI border gateways
   - Verify route appears in remote region's RR (via BGP service in that region)
6. Test cross-region peering with two GoBGP instances simulating two regions

**Acceptance**: Cross-region peering works via API. Routes propagate across regions. MED values reflect latency. Peering response includes `cross_region`, `measured_latency_ms`, `regions`.

**Dependencies**: Phase 1 complete

---

### Step 2.2: Cross-Region BFD and Failover (Week 19–21)

**What**: Implement FR-6.3 — automatic failover when inter-region connectivity is lost.

**Tasks**:
1. Deploy FRR `bfdd` sidecar alongside `vpc-interconnect-bgp`
2. Implement `internal/bgp/bfd.go` — BFD state monitor:
   - Watch FRR `bfdd` Unix socket for session state changes
   - On BFD down: trigger BGP session teardown for the affected peer
   - On BFD up: allow BGP session re-establishment
3. Implement latency measurement:
   - Agent on border leaves measures RTT to peer border leaves (ICMP echo every 30s)
   - Reports latency to BGP service via gRPC
   - BGP service adjusts MED values on routes
4. Extend controller to handle `degraded` state:
   - When BFD reports cross-region peer down → transition cross-region peerings to `degraded`
   - Emit `cross_region_connectivity_lost` event
   - When BFD restores → re-advertise routes → transition back to `active`
   - Emit `cross_region_connectivity_restored` event
5. Test: simulate DCI failure (kill BFD session) → verify peering degrades → restore → verify recovery

**Acceptance**: BFD detects cross-region failure in < 300ms. Peering transitions to degraded. Events emitted. Recovery is automatic.

**Dependencies**: Step 2.1

---

### Step 2.3: XDP Fast-Path ACL Enforcement (Week 21–24)

**What**: Implement Tier 1 ACL enforcement using XDP/eBPF for high-performance packet filtering.

**Tasks**:
1. Write XDP program in C (`internal/xdp/acl.c`):
   - Parse outer Ethernet + IP + UDP + VXLAN header
   - Extract inner 5-tuple (src IP, dst IP, protocol, src port, dst port) + VNI
   - Look up (VNI, 5-tuple) in BPF hash map → allow/drop
   - Increment per-peering byte/packet counters in BPF array map
   - XDP_PASS or XDP_DROP
2. Implement Go XDP loader in `internal/agent/xdp.go` using `cilium/ebpf`:
   - Load compiled XDP ELF
   - Attach to host NIC at XDP hook
   - Populate BPF maps from route policy ACL rules
   - Read counters for metering
3. Extend agent to choose enforcement tier:
   - If XDP available (kernel 5.10+, NIC supports XDP) → Tier 1
   - Otherwise → fall back to nftables (Tier 0)
4. Implement ACL rule translation: `RoutePolicy` → BPF map entries
5. Benchmark: measure throughput with XDP vs nftables
6. Test: ACL deny rule → packet dropped at XDP → counter incremented

**Acceptance**: XDP ACL processes packets at 10+ Gbps/core. BPF maps correctly populated from route policy. Fallback to nftables works.

**Dependencies**: Step 1.10 (agent)

---

### Step 2.4: Metering Daemon (Week 22–25)

**What**: Implement `vpc-interconnect-meter` — billing-grade data transfer metering per FR-9.

**Tasks**:
1. Implement `cmd/meter/main.go` and `internal/meter/`:
   - Read byte/packet counters from XDP BPF maps (if available) or kernel interface stats
   - Aggregate per peering ID, per direction, per 1-minute interval
   - Distinguish same-region vs cross-region traffic (based on peering metadata)
2. Implement billing event emission:
   - Produce events to Kafka/NATS topic: `{peering_id, account_id, vpc_id, direction, region_type, bytes, timestamp}`
   - At-least-once delivery with idempotent consumers (dedup by `peering_id + timestamp`)
3. Implement counter persistence:
   - Write aggregated counters to PostgreSQL (or time-series DB) for the metrics API
   - Retain raw 1-minute data for 30 days, hourly rollups for 1 year
4. Test:
   - Generate traffic → verify counters match
   - Kill meter daemon → restart → counters resume (no double-counting due to BPF map persistence)
   - Verify billing events arrive in Kafka topic with correct schema

**Acceptance**: Metering accurate to within 0.1% of actual traffic. Billing events emitted every minute. No data loss on daemon restart.

**Dependencies**: Step 2.3 (XDP counters)

---

### Step 2.5: Customer-Facing Metrics and Usage API (Week 25–27)

**What**: Implement FR-8.1 (`GET /v1/metrics/peerings/{id}`) and FR-9.3 (`GET /v1/usage/peerings/{id}`).

**Tasks**:
1. Implement metrics API handler:
   - Query aggregated counters from step 2.4
   - Support `period` parameter: `5m`, `1h`, `24h`
   - Compute `packets_dropped` with breakdown by reason (acl_deny, rate_limit, no_route)
   - Return JSON per FR-8.1 schema
2. Implement usage API handler:
   - Query time-series data with `start_time`, `end_time`, `granularity` (hourly/daily)
   - Return per-direction, per-region-type breakdown
   - Paginate large time ranges
3. Extend Prometheus metrics export:
   - Per-peering throughput gauges
   - BGP convergence histograms
   - Peering state gauges
4. Build Grafana dashboard:
   - Per-peering throughput graphs
   - Peering health overview (healthy/degraded/down counts)
   - BGP session status
   - API latency percentiles
5. Test: generate known traffic → verify metrics API returns matching numbers → verify usage API matches billing events

**Acceptance**: Metrics API returns data within 60s of traffic. Usage API returns accurate billing data. Grafana dashboard shows all key metrics.

**Dependencies**: Step 2.4

---

### Step 2.6: Bandwidth Limiting (Week 26–28)

**What**: Implement FR-5.1 `bandwidth_limit_mbps` enforcement at the data plane.

**Tasks**:
1. Extend XDP program with token-bucket rate limiter:
   - Per-peering rate limit stored in BPF map
   - Packets exceeding rate are XDP_DROP'd
   - Emit `bandwidth_limit_exceeded` event (sampled)
2. Alternative path using Linux `tc` (traffic control) for hosts without XDP:
   - Agent programs `tc` HTB qdisc + filter on VRF interface
3. Extend agent to program bandwidth limits from route policy
4. Test: set 100 Mbps limit → generate 200 Mbps → verify ~100 Mbps passes, rest dropped
5. Test: update bandwidth limit on active peering → effective within 10 seconds

**Acceptance**: Bandwidth limiting accurate to within 5%. Hot-update within 10 seconds. Events emitted on violation.

**Dependencies**: Step 2.3

---

### Step 2.7: Phase 2 Integration Testing (Week 28–30)

**What**: End-to-end testing of all Phase 2 features.

**Tasks**:
1. Cross-region peering: create → active → traffic flows → measure latency → delete
2. Cross-region failover: simulate DCI failure → degraded → restore → active
3. XDP ACL: deny rule → traffic blocked → counter incremented → event emitted
4. Metering: generate traffic → metrics API shows correct throughput → usage API matches
5. Bandwidth limiting: set limit → excess traffic dropped → update limit → new limit enforced
6. Scale test: 500 VPCs, 2000 peerings, multi-region — verify system stability
7. Billing pipeline: verify billing events arrive correctly in Kafka consumer

**Acceptance**: All Phase 2 functional requirements pass. Scale test stable. Metering accurate.

**Dependencies**: Steps 2.1–2.6

---

## Phase 3: Transit Gateway + Hub-Spoke (Weeks 31–40)

> **Goal**: Transit gateway for hub-and-spoke topologies, DPU offload, Terraform provider.

---

### Step 3.1: Transit Gateway Data Model and API (Week 31–33)

**What**: Implement FR-7 — transit gateway CRUD and VPC attachment.

**Tasks**:
1. Database migration: `transit_gateways` table, `transit_gateway_attachments` table, `transit_gateway_routes` table
2. Implement `TransitGateway`, `Attachment` domain models
3. Implement API handlers:
   - `POST /v1/transit-gateways`: create transit gateway with auto-assigned ASN
   - `GET /v1/transit-gateways/{id}`: get details
   - `POST /v1/transit-gateways/{id}/attachments`: attach VPC
   - `DELETE /v1/transit-gateways/{id}/attachments/{att_id}`: detach VPC
   - `GET /v1/transit-gateways/{id}/route-table`: consolidated route table
4. Implement attachment route propagation:
   - `full`: import all VPC CIDRs into transit gateway route table, redistribute to other attached VPCs
   - `none`: no automatic propagation, manual route management only

**Acceptance**: Transit gateway CRUD works. VPC attachment creates peering-like connectivity through the hub. Route table shows consolidated view.

**Dependencies**: Phase 2 complete

---

### Step 3.2: Hub-Spoke Routing and Spoke Isolation (Week 33–35)

**What**: Implement FR-7.4 — spoke isolation and hub-spoke routing patterns.

**Tasks**:
1. Implement route table association:
   - Each attachment can be associated with a route table
   - Multiple route tables per transit gateway (e.g., "hub-table", "spoke-table")
2. Implement spoke isolation:
   - Spoke VPCs only see routes to the hub VPC (not to other spokes)
   - Hub VPC sees routes to all spokes
   - Controlled by which route table each attachment is associated with
3. Implement shared services VPC pattern:
   - One VPC's routes visible to all attached VPCs
   - Other VPCs' routes not visible to each other
4. Extend controller to handle transit gateway provisioning (RT setup for hub VRF)
5. Test hub-spoke topology: 1 hub + 5 spokes → spokes reach hub but not each other

**Acceptance**: Hub-spoke isolation works. Shared services pattern works. Route tables correctly control visibility.

**Dependencies**: Step 3.1

---

### Step 3.3: Multi-Region Transit Gateway (Week 35–37)

**What**: Transit gateway with spokes in multiple regions.

**Tasks**:
1. Allow VPC attachments from different regions than the transit gateway's home region
2. Cross-region route propagation through transit gateway hub
3. MED-based path selection through transit gateway
4. Test: transit gateway in us-east-1, spokes in us-east-1 and eu-west-1

**Acceptance**: Multi-region transit gateway routes traffic between regions through hub. Latency-aware path selection works.

**Dependencies**: Steps 3.2, 2.1 (cross-region)

---

### Step 3.4: SmartNIC/DPU Offload (Week 36–38)

**What**: Hardware ACL and VXLAN offload for hosts with DPUs.

**Tasks**:
1. Implement DPU detection in agent (`internal/agent/dpu.go`):
   - Detect NVIDIA BlueField, AMD Pensando, or Intel IPU via PCI/sysfs
   - Query DPU capabilities (ACL offload, VXLAN offload)
2. Implement DPU ACL programming:
   - Translate route policy ACL rules to DPU hardware ACL entries
   - Use DPU vendor SDK or OVS-DPDK flow rules
3. Implement VXLAN offload detection:
   - If DPU supports VXLAN offload, configure it for the VPC's VNI
   - Encap/decap handled by DPU, not kernel
4. Extend agent tier selection: DPU present → Tier 2, else XDP → Tier 1, else nftables → Tier 0
5. Test with BlueField DPU: verify ACL offload, verify VXLAN offload, benchmark

**Acceptance**: DPU detected and utilized when present. ACL enforcement at hardware speed. Graceful fallback when DPU not available.

**Dependencies**: Step 2.3 (XDP as baseline)

---

### Step 3.5: Terraform Provider (Week 37–39)

**What**: Build a Terraform provider for VPC peering and transit gateway resources.

**Tasks**:
1. Create `terraform-provider-vpcinterconnect` using Terraform Plugin Framework (Go)
2. Implement resources:
   - `vpcinterconnect_vpc`
   - `vpcinterconnect_peering`
   - `vpcinterconnect_transit_gateway`
   - `vpcinterconnect_transit_gateway_attachment`
3. Implement data sources:
   - `vpcinterconnect_vpc` (read-only lookup)
   - `vpcinterconnect_peering` (read-only lookup)
   - `vpcinterconnect_effective_routes`
4. Handle peering state machine in Terraform:
   - `Create` → wait for `active` state (poll with backoff)
   - `Delete` → wait for `deleted` state
   - Cross-account peering → Terraform on accepter side calls `accept`
5. Write acceptance tests using Terraform test framework
6. Publish provider documentation

**Acceptance**: `terraform apply` creates VPC + peering end-to-end. `terraform destroy` cleans up. State refresh works correctly.

**Dependencies**: Phase 1 API stable

---

### Step 3.6: Phase 3 Integration Testing and Launch Prep (Week 39–40)

**What**: Final integration testing, documentation, and launch readiness.

**Tasks**:
1. End-to-end test: Terraform creates transit gateway + hub-spoke topology → traffic flows → Terraform destroys
2. Scale test at full target: 1000 VPCs, 5000 peerings, transit gateway with 100 attachments
3. Failure testing: transit gateway controller failure, DPU failover to software path
4. API documentation update: all Phase 3 endpoints
5. Terraform provider documentation and examples
6. Operations runbook update: transit gateway troubleshooting, DPU diagnostics
7. Customer-facing documentation: getting started guide, architecture overview, best practices

**Acceptance**: All functional requirements (FR-1 through FR-10) pass. Scale targets met. Documentation complete.

**Dependencies**: Steps 3.1–3.5

---

## Milestone Summary

| Milestone | Week | Key Deliverable | FR Coverage |
|---|---|---|---|
| Repository bootstrap | 1 | Go project, CI, proto definitions | — |
| VPC API | 4 | Full VPC CRUD | FR-1 |
| Peering API | 5 | Peering CRUD + cross-account + events | FR-2, FR-3, FR-4, FR-8.2 |
| BGP service | 7 | EVPN Type-5 route injection/withdrawal | FR-2 (provisioning) |
| Host agent | 9 | VRF/VXLAN/ACL programming | FR-5 (enforcement) |
| Controller | 10 | End-to-end orchestration + reconciliation | FR-2 (complete) |
| **Phase 1 GA** | **16** | **Single-region peering, production-ready** | **FR-1–5, FR-8.2–3, FR-10** |
| Cross-region | 21 | Multi-region peering + BFD failover | FR-6 |
| XDP + metering | 25 | Fast-path ACLs + billing metering | FR-5, FR-9 |
| Metrics API | 27 | Customer-facing metrics + usage | FR-8.1, FR-9.3 |
| **Phase 2 GA** | **30** | **Multi-region + observability + billing** | **FR-6, FR-8.1, FR-9** |
| Transit gateway | 35 | Hub-spoke topologies | FR-7 |
| DPU offload | 38 | Hardware acceleration | NFR-1 (perf) |
| Terraform | 39 | Infrastructure-as-code | — |
| **Phase 3 GA** | **40** | **Full product** | **FR-7, all FRs complete** |
