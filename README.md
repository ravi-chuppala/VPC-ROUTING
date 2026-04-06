# VPC Interconnect

Private, high-performance, fabric-native connectivity between VPCs — any region, any topology, at line-rate.

Customers think in VPCs. Connecting VPC-A to VPC-B should be easy and transparent. This system handles route exchange, encapsulation, policy enforcement, and failover — all using the production EVPN-VXLAN network fabric.

## Architecture

The system is organized into three planes:

```
┌─────────────────────────────────────────────────────────────────┐
│                      Management Plane                           │
│  ┌──────────────────┐  ┌──────────────────────────────────┐    │
│  │ vpc-interconnect  │  │   vpc-interconnect-controller    │    │
│  │      -api         │  │   (peering lifecycle, reconcile) │    │
│  │  (REST + gRPC)    │  └──────────────┬───────────────────┘    │
│  └────────┬─────────┘                  │                        │
├─────────────────────────────────────────────────────────────────┤
│                       Control Plane                             │
│  ┌──────────────────────────────────┐                          │
│  │     vpc-interconnect-bgp         │                          │
│  │  (embedded GoBGP, EVPN Type-5   │◄──── Fabric Route        │
│  │   route injection/withdrawal)    │      Reflectors          │
│  └──────────────────────────────────┘                          │
├─────────────────────────────────────────────────────────────────┤
│                        Data Plane                               │
│  ┌────────────────┐  ┌──────────────┐  ┌───────────────────┐  │
│  │ vpc-interconnect│  │vpc-interconnect│ │ vpc-interconnect  │  │
│  │    -agent       │  │   -meter      │ │     -xdp          │  │
│  │ (VRF, VXLAN,   │  │ (byte/pkt    │ │ (ACL enforcement, │  │
│  │  netlink)       │  │  counters)   │ │  fast-path)        │  │
│  └────────────────┘  └──────────────┘  └───────────────────┘  │
│                                                                 │
│  ═══════════════ EVPN-VXLAN Fabric (switches/ASICs) ══════════ │
└─────────────────────────────────────────────────────────────────┘
```

**Management Plane** — REST API for customers, peering lifecycle orchestration, PostgreSQL state store.

**Control Plane** — BGP/EVPN route injection and withdrawal. Embeds GoBGP as a Go library. Injects EVPN Type-5 (IP Prefix) routes into the fabric using RT-based inter-VRF route leaking.

**Data Plane** — Per-host agent programs Linux VRFs, VXLAN interfaces, and nftables/XDP ACL rules via netlink. Traffic is forwarded by the EVPN-VXLAN fabric at line rate — no software hop for standard peering.

## Project Structure

```
├── cmd/
│   ├── api/                  # REST API server entrypoint
│   ├── controller/           # Peering orchestrator + reconciliation loop
│   ├── bgp/                  # BGP service (EVPN route management)
│   └── agent/                # Per-host agent (VRF/VXLAN/ACL programming)
├── internal/
│   ├── api/                  # HTTP handlers, router, response envelope
│   ├── auth/                 # JWT/API-key auth, ownership checks, rate limiter
│   ├── model/                # Domain types, CIDR validation, route policy, errors
│   ├── store/                # Store interfaces + in-memory + PostgreSQL impls
│   ├── vni/                  # VNI allocator (24-bit partitioned space)
│   ├── bgp/                  # EVPN Type-5 route builder, BGP service interface
│   ├── agent/                # Netlink VRF/VXLAN/ACL manager + drift reconciler
│   └── controller/           # Peering provisioner + reconciliation loop
├── proto/                    # Protobuf definitions (gRPC service contracts)
│   ├── vpc/v1/               # VpcService
│   ├── peering/v1/           # PeeringService
│   └── internal/v1/          # BgpControlService, AgentControlService
├── migrations/               # PostgreSQL schema migrations
├── deploy/                   # Dockerfiles, systemd unit
├── test/e2e/                 # End-to-end integration tests
├── docs/design/              # Architecture doc, functional spec, execution plan
├── Makefile                  # build, test, lint, clean
└── docker-compose.yml        # PostgreSQL for local development
```

## Key Design Decisions

- **Fabric-native**: Routes are injected directly into the existing EVPN-VXLAN fabric as Type-5 routes. No overlay-on-overlay. Traffic is forwarded by switch ASICs at line rate.
- **RT-based isolation**: Peering uses Route Target import/export to leak routes between VRFs. No transit VNI per peering — avoids VNI space exhaustion at scale.
- **Reconciliation-driven**: Both the controller and agent use reconciliation loops to continuously ensure desired state matches actual state. Handles missed events, restarts, and drift.
- **Sub-1s convergence**: BFD (300ms detection) + immediate BGP route withdrawal = ~400ms end-to-end failover.

## Dependencies

| Dependency | Version | Purpose |
|---|---|---|
| Go | 1.22+ | Language runtime |
| `github.com/google/uuid` | v1.6.0 | UUID generation for resource IDs |
| `github.com/jackc/pgx/v5` | v5.9.1 | PostgreSQL driver and connection pool |
| `golang.org/x/time` | v0.15.0 | Token-bucket rate limiting (`rate.Limiter`) |
| PostgreSQL | 16+ | State store (VPCs, peerings, events, routes) |

### Future dependencies (per execution plan)

| Dependency | Purpose | Phase |
|---|---|---|
| `github.com/osrg/gobgp/v3` | Embedded BGP/EVPN route injection | 1 (production) |
| `github.com/vishvananda/netlink` | Linux VRF, VXLAN, route programming | 1 (production) |
| `github.com/cilium/ebpf` | XDP program loading for ACL enforcement | 2 |
| `google.golang.org/grpc` | Internal gRPC between services | 1 (production) |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | REST-to-gRPC proxy | 1 (production) |

## Build

### Prerequisites

- Go 1.22 or later
- Docker and Docker Compose (for PostgreSQL)

### Build all binaries

```bash
make build
```

Produces four binaries in `bin/`:
- `bin/api` — REST API server
- `bin/controller` — Peering lifecycle orchestrator
- `bin/bgp` — BGP/EVPN route management service
- `bin/agent` — Per-host VRF/VXLAN/ACL agent

### Run tests

```bash
make test
```

Runs 57 tests across 8 packages with the Go race detector enabled:

| Package | Tests | What's covered |
|---|---|---|
| `internal/model` | 24 | CIDR validation (RFC 1918, /16-/28, overlap), route policy evaluation |
| `internal/api` | 23 | VPC CRUD, peering lifecycle, cross-account, events, effective routes |
| `internal/auth` | 5 | Context extraction, ownership checks, bearer parsing, rate limiter |
| `internal/vni` | 5 | Allocation, encode/decode, reserved ranges, concurrent safety |
| `internal/bgp` | 4 | EVPN Type-5 route construction, RT config, inject/withdraw |
| `internal/agent` | 5 | VRF lifecycle, routes, ACL programming, reconciler drift detection |
| `internal/controller` | 4 | Provision, deprovision, reconciliation loop, pending expiry |
| `test/e2e` | 4 | Full lifecycle, cross-account, cross-region, error scenarios |

### Lint

```bash
make lint
```

### Test coverage

```bash
make test-coverage
# Opens coverage.html in browser
```

### Clean

```bash
make clean
```

## Run

### Start PostgreSQL (local development)

```bash
docker-compose up -d
```

This starts PostgreSQL 16 on port 5432 with database `vpcinterconnect`, user `vpc`, password `vpc_dev_password`. The initial migration runs automatically.

### Start the API server

```bash
PORT=8080 ./bin/api
```

The server starts on port 8080 with in-memory stores (no PostgreSQL required for basic testing).

### Docker

Build and run any service as a container:

```bash
docker build -f deploy/Dockerfile.api -t vpc-interconnect-api .
docker run -p 8080:8080 vpc-interconnect-api
```

## API Reference

All endpoints are versioned under `/v1/`. Requests require an authenticated account context. Responses use a consistent JSON envelope.

### VPC Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/vpcs` | Register a VPC |
| `GET` | `/v1/vpcs` | List VPCs (filtered by `region_id`) |
| `GET` | `/v1/vpcs/{id}` | Get VPC details |
| `DELETE` | `/v1/vpcs/{id}` | Delete VPC (must have zero peerings) |
| `GET` | `/v1/vpcs/{id}/effective-routes` | Consolidated route table across all peerings |

### Peering Endpoints

| Method | Path | Description |
|---|---|---|
| `POST` | `/v1/peerings` | Create peering (same-account auto-accepts) |
| `GET` | `/v1/peerings` | List peerings (filter by `vpc_id`, `state`) |
| `GET` | `/v1/peerings/{id}` | Get peering with health status |
| `PATCH` | `/v1/peerings/{id}` | Update route policy or direction |
| `DELETE` | `/v1/peerings/{id}` | Delete peering (either side can delete) |
| `POST` | `/v1/peerings/{id}/accept` | Accept cross-account peering |
| `POST` | `/v1/peerings/{id}/reject` | Reject cross-account peering |
| `GET` | `/v1/peerings/{id}/routes` | List effective routes for peering |
| `POST` | `/v1/peerings/{id}/routes` | Add static route or withdraw prefix |
| `GET` | `/v1/peerings/{id}/events` | Peering event log |

### Health

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz` | Liveness check |

### Example: Create a VPC

```bash
curl -X POST http://localhost:8080/v1/vpcs \
  -H "Content-Type: application/json" \
  -d '{
    "name": "production",
    "region_id": "us-east-1",
    "cidr_blocks": ["10.0.0.0/16"]
  }'
```

Response:
```json
{
  "data": {
    "id": "a1b2c3d4-...",
    "account_id": "...",
    "region_id": "us-east-1",
    "name": "production",
    "cidr_blocks": ["10.0.0.0/16"],
    "vni": 4096,
    "vrf_name": "vpc-a1b2c3d4",
    "state": "active",
    "peering_count": 0,
    "created_at": "2026-04-06T10:00:00Z"
  },
  "request_id": "req-..."
}
```

### Example: Create a peering

```bash
curl -X POST http://localhost:8080/v1/peerings \
  -H "Content-Type: application/json" \
  -d '{
    "requester_vpc_id": "<vpc-a-id>",
    "accepter_vpc_id": "<vpc-b-id>"
  }'
```

Same-account peerings auto-accept and transition to `active` immediately. Cross-account peerings require the accepter to call `POST /v1/peerings/{id}/accept`.

### Response Envelope

**Success (single resource)**:
```json
{
  "data": { ... },
  "request_id": "req-abc123"
}
```

**Success (list)**:
```json
{
  "data": [ ... ],
  "pagination": { "next_page_token": "...", "total_count": 42 },
  "request_id": "req-abc123"
}
```

**Error**:
```json
{
  "error": { "code": "CIDR_OVERLAP", "message": "...", "details": { ... } },
  "request_id": "req-abc123"
}
```

### Error Codes

| HTTP | Code | Description |
|---|---|---|
| 400 | `INVALID_INPUT` | Malformed request |
| 400 | `INVALID_CIDR` | CIDR not RFC 1918 or outside /16-/28 |
| 401 | `UNAUTHENTICATED` | Missing or invalid credentials |
| 403 | `PERMISSION_DENIED` | Caller does not own the resource |
| 404 | `NOT_FOUND` | Resource does not exist |
| 409 | `CIDR_OVERLAP` | VPC CIDR overlaps with existing VPC |
| 409 | `DUPLICATE_PEERING` | Peering already exists between these VPCs |
| 409 | `HAS_ACTIVE_PEERINGS` | Cannot delete VPC with active peerings |
| 409 | `INVALID_STATE` | Operation not allowed in current state |
| 422 | `QUOTA_EXCEEDED` | Resource limit reached |
| 422 | `VNI_EXHAUSTED` | No VNIs available in target region |
| 429 | `RATE_LIMITED` | Too many requests |

### Rate Limits

| Category | Limit |
|---|---|
| Mutating operations (POST, PATCH, DELETE) | 60/min per account |
| Read operations (GET) | 300/min per account |

## Peering Lifecycle

```
[Create peering request]
       │
       ▼
 pending_acceptance ──(cross-account: accepter calls /accept)──┐
       │ (same-account: auto-accept)                           │
       ▼                                                       ▼
   provisioning ◄──────────────────────────────────────────────┘
       │
       ├── Controller configures RT import/export
       ├── BGP service injects EVPN Type-5 routes
       ├── Agent programs VRF + ACL rules on hosts
       │
       ▼
     active ◄──────── (auto-heal) ──── degraded
       │                                    ▲
       ├── (failure detected) ──────────────┘
       ├── (user deletes) ─► deleting ─► deleted
       └── (user updates policy) ─► re-evaluate routes ─► active
```

Pending peerings not accepted within 7 days are automatically expired by the reconciler.

## Route Policy

Each peering has a configurable route policy:

```json
{
  "allowed_prefixes": ["10.2.0.0/24"],
  "denied_prefixes": ["10.2.255.0/24"],
  "max_prefixes": 100,
  "bandwidth_limit_mbps": 1000
}
```

Evaluation order (per prefix):
1. **Denied** — if prefix matches any denied entry, reject
2. **Allowed** — if whitelist is set and prefix is not in it, filter
3. **Max-prefix** — if count exceeds limit, filter and emit alert
4. **Accept**

## VNI Allocation

VNIs use a 24-bit space partitioned as:

| Bits | Width | Field | Capacity |
|---|---|---|---|
| 23-20 | 4 bits | Region ID | 16 regions |
| 19-10 | 10 bits | Tenant shard (hash of account UUID) | 1,024 shards |
| 9-0 | 10 bits | Per-tenant VPC sequence | 1,024 VPCs per shard |

Reserved ranges: VNI 1-4095 (VLAN overlap) and 16,000,000+ (future use).

## Design Documents

Detailed design docs are in `docs/design/`:

- [Technical Architecture](docs/design/vpc-interconnect-architecture.md) — data plane, control plane, management plane, security, HA, scale
- [Functional Specification](docs/design/vpc-interconnect-functional-spec.md) — every API operation with inputs, outputs, errors, and constraints
- [Execution Plan](docs/design/vpc-interconnect-execution-plan.md) — 19 steps across 3 phases with acceptance criteria

## Roadmap

| Phase | Timeline | What Ships |
|---|---|---|
| **Phase 1** | Weeks 1-16 | Single-region VPC peering via REST API |
| **Phase 2** | Weeks 17-30 | Multi-region peering, XDP ACLs, metering, billing |
| **Phase 3** | Weeks 31-40 | Transit gateway, hub-spoke topologies, DPU offload, Terraform provider |
