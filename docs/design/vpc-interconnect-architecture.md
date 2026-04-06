# VPC Interconnect — Technical Architecture

> Private, high-performance, fabric-native connectivity between VPCs — any region, any topology, at line-rate.

**Status**: Draft  
**Date**: 2026-04-06  
**Target Language**: Go  

---

## Table of Contents

1. [System Architecture Overview](#1-system-architecture-overview)
2. [Data Plane Design](#2-data-plane-design)
3. [Control Plane Design](#3-control-plane-design)
4. [Management Plane / API](#4-management-plane--api)
5. [Multi-Region Architecture](#5-multi-region-architecture)
6. [Security Model](#6-security-model)
7. [Observability](#7-observability)
8. [Failure Modes & HA](#8-failure-modes--ha)
9. [Scale Design](#9-scale-design)
10. [Implementation Roadmap](#10-implementation-roadmap)
11. [Appendix A: Architecture Decision Records](#appendix-a-architecture-decision-records)
12. [Appendix B: Go Dependencies](#appendix-b-go-dependencies)

---

## 1. System Architecture Overview

### 1.1 Three-Plane Separation

```
┌─────────────────────────────────────────────────────────────────┐
│                      Management Plane                           │
│  ┌──────────────────┐  ┌──────────────────────────────────┐    │
│  │ vpc-interconnect  │  │   vpc-interconnect-controller    │    │
│  │      -api         │  │   (peering lifecycle, reconcile) │    │
│  │  (REST + gRPC)    │  └──────────────┬───────────────────┘    │
│  └────────┬─────────┘                  │                        │
│           │            ┌───────────────┘                        │
│           ▼            ▼                                        │
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

**Data Plane** — The forwarding path. Packets move between VPCs via VXLAN encapsulation over the existing EVPN-VXLAN fabric. No software process sits in the hot path for standard peering; switch ASICs or SmartNICs handle encap/decap at line rate. Where policy enforcement (ACLs, metering) is required, an XDP fast-path or DPDK appliance is inserted.

**Control Plane** — Route computation and distribution. A cluster of GoBGP instances (embedded as a Go library, not CLI) participates in the EVPN-VXLAN fabric as route reflector clients. When a peering is established, the control plane injects EVPN Type-5 (IP Prefix) routes into the fabric, scoped by Route Distinguisher and Route Target, causing the fabric to learn reachability between the two VPCs. BFD sessions detect failures and trigger sub-second reconvergence.

**Management Plane** — The customer-facing API and internal orchestration. A Go service exposes gRPC (internal) and REST (customer-facing, via grpc-gateway) APIs for peering lifecycle CRUD. It validates requests, enforces quotas and policies, persists state to PostgreSQL, and drives the control plane by instructing GoBGP instances to advertise or withdraw routes.

### 1.2 Key Decision: Fabric-Native vs. Overlay-on-Overlay

**Decision**: Fabric-native. Inter-VPC routes are injected directly into the existing EVPN-VXLAN fabric as Type-5 routes. Traffic is forwarded by leaf switches natively — no additional overlay tunnel, no extra encapsulation hop.

**Trade-off**: This couples the product to the fabric's BGP/EVPN implementation. The alternative (a separate overlay mesh, e.g., WireGuard tunnels between hypervisors) would be fabric-agnostic but adds latency, MTU tax, and operational complexity. For infrastructure we own, fabric-native is the correct choice.

See [ADR-001](#adr-001-fabric-native-route-leaking-vs-overlay-on-overlay).

### 1.3 Component Inventory

| Component | Language | Role | Deployment |
|---|---|---|---|
| `vpc-interconnect-api` | Go | REST/gRPC management plane service | N+1 replicas, stateless |
| `vpc-interconnect-controller` | Go | Peering lifecycle orchestration, reconciliation | Leader-elected singleton |
| `vpc-interconnect-bgp` | Go (embeds GoBGP) | EVPN route injection/withdrawal, RR client | N+1 replicas |
| `vpc-interconnect-agent` | Go | Per-host: programs VRFs, VXLAN interfaces, netfilter rules via netlink | Per-host singleton (systemd) |
| `vpc-interconnect-meter` | Go | Per-host or per-ToR data-transfer metering daemon | Per-host singleton |
| `vpc-interconnect-xdp` | C + Go loader | XDP fast-path for ACL enforcement and metering | Per-host (loaded by agent) |

---

## 2. Data Plane Design

### 2.1 VXLAN Encapsulation and VNI Allocation

The 24-bit VNI space (~16.7 million) is partitioned:

| Bits | Width | Field | Capacity |
|---|---|---|---|
| 23–20 | 4 bits | Region ID | 16 regions |
| 19–10 | 10 bits | Tenant shard (consistent hash of account UUID) | 1,024 shards |
| 9–0 | 10 bits | Per-tenant VPC sequence | 1,024 VPCs per shard |

This is a logical allocation scheme. The actual mapping is maintained by a central VNI allocation service (part of the management plane) backed by a database with a unique constraint. The bitfield convention makes VNIs human-debuggable in packet captures.

**Critical**: Each VPC already has a VNI in the existing fabric. The interconnect product does **not** allocate new VNIs for peering tunnels. Instead, it uses EVPN Type-5 routes with inter-VRF route leaking: traffic from VPC-A destined for VPC-B's prefix is encapsulated with VPC-B's VNI by the originating leaf (or host VTEP).

**No transit VNI**. Peering uses route leaking between VRFs, not a shared transit VNI. This avoids a single VNI becoming a scaling bottleneck and keeps tenant isolation clean. See [ADR-003](#adr-003-rt-based-isolation-vs-vni-per-peering).

### 2.2 Forwarding Path

**Same-region peering (hot path)**:

```
VM (VPC-A)
    │ dst: 10.2.0.5 (VPC-B prefix)
    ▼
Host kernel / ToR leaf
    │ VRF lookup in VPC-A's VRF
    │ Matches leaked Type-5 route: 10.2.0.0/16 → next-hop VTEP-B, VNI=VPC-B
    ▼
VXLAN encap
    │ Outer: src=VTEP-A, dst=VTEP-B, VNI=VPC-B's VNI
    │ Forwarded over underlay ECMP
    ▼
Destination leaf / host
    │ VXLAN decap, deliver to VPC-B's VRF
    ▼
VM (VPC-B)
```

No software hop in the forwarding path for standard peering. The fabric's existing ECMP handles load balancing across underlay paths.

**Cross-region peering**: Same flow, but the underlay path traverses inter-region DCI links. The EVPN Type-5 route carries a next-hop that may be a border leaf or DCI gateway VTEP. The fabric's existing multi-site EVPN handles underlay reachability.

### 2.3 Fast-Path Tiers

| Tier | Technology | Use Case | Throughput |
|---|---|---|---|
| 0 | Switch ASIC (native fabric) | Default, no-policy peering | Line rate |
| 1 | XDP (eBPF on host) | ACL enforcement, metering at host | 10–40 Gbps/core |
| 2 | DPDK appliance (service chain) | Complex policy, deep inspection, NAT | 100 Gbps+ per appliance |

**XDP program design**: An eBPF program attached to the host's physical NIC at the XDP hook point. It inspects the inner VXLAN header, matches VNI + 5-tuple against a BPF map populated by the `vpc-interconnect-agent`, and performs:

1. ALLOW/DROP for ACL enforcement
2. Byte/packet counter increment for metering
3. Redirect or pass to kernel stack

The Go agent loads and manages the XDP program using the `cilium/ebpf` library.

**SmartNIC/DPU offload**: For hosts with NVIDIA BlueField or similar DPUs, VXLAN encap/decap and ACL matching can be offloaded to the DPU via OVS-DPDK or the DPU's embedded ARM cores. The agent detects DPU presence and delegates via the DPU's management API.

### 2.4 MTU Handling

VXLAN adds 50 bytes of overhead:

```
14 (outer Ethernet) + 20 (outer IP) + 8 (UDP) + 8 (VXLAN header) = 50 bytes
```

- Fabric underlay: jumbo frames, MTU 9214 or 9216
- Inner VM-facing MTU: 1500 (standard) or up to 9000 (jumbo, opt-in per VPC)
- The management plane validates that peered VPCs have compatible MTU settings and warns on mismatch
- Path MTU Discovery enabled; host VTEP sets DF bit on outer IP header and responds with ICMP Fragmentation Needed if the inner packet exceeds (underlay MTU − 50)

---

## 3. Control Plane Design

### 3.1 GoBGP Integration Architecture

GoBGP is embedded as a Go library (`github.com/osrg/gobgp/v3`), not run as a separate daemon. The `vpc-interconnect-bgp` service instantiates a `gobgp.BgpServer` and interacts via its in-process API.

See [ADR-002](#adr-002-embedded-gobgp-vs-frr-for-evpn-route-injection).

Key GoBGP packages:

| Package | Purpose |
|---|---|
| `github.com/osrg/gobgp/v3/api` (apipb) | Protobuf API: `AddPath`, `DeletePath`, `ListPath`, `MonitorTable` |
| `github.com/osrg/gobgp/v3/pkg/packet/bgp` | BGP/EVPN message construction (`EVPN_IP_PREFIX` for Type-5) |
| `github.com/osrg/gobgp/v3/pkg/server` | `BgpServer` struct for embedding |

Each `vpc-interconnect-bgp` instance peers with the fabric's route reflectors as an EVPN RR client. It does **not** become a route reflector itself — it is a route injector/consumer.

### 3.2 EVPN Route Types

| Route Type | RFC | Purpose |
|---|---|---|
| Type-2 (MAC/IP Advertisement) | RFC 7432 | Not directly injected; consumed to learn VM placement for host-based forwarding |
| Type-5 (IP Prefix) | RFC 9136 | **Primary route type.** Injected to advertise VPC-A's prefixes into VPC-B's VRF and vice versa |

**Type-5 Route Construction**:

```
Route Distinguisher:  <region-id>:<vpc-id>          (Type 1 RD)
Ethernet Tag ID:      0
IP Prefix Length:     /16, /24, etc.                (from VPC's CIDR)
IP Prefix:            VPC subnet                    (e.g., 10.2.0.0)
Gateway IP:           0.0.0.0                       (recursive resolution via underlay)
VNI/Label:            Target VPC's L3 VNI
Route Targets:
  - Export RT:        target:<account-id>:<vpc-id>
  - Import RT:        Added to peer VPC's VRF when peering is created
```

### 3.3 VRF-to-VPC Mapping

Each VPC maps 1:1 to a Linux VRF (on hosts) and a VRF instance on leaf switches. Convention:

- VRF name: `vpc-<vpc-uuid-short>` (e.g., `vpc-a1b2c3d4`)
- L3 VNI: the VPC's allocated VNI

**Route leaking mechanism** — when a peering is created between VPC-A and VPC-B:

1. Controller tells the BGP service to add VPC-B's export RT to VPC-A's import RT list (and vice versa for bidirectional peering)
2. GoBGP re-evaluates routes: VPC-B's Type-5 prefixes now match VPC-A's import policy
3. GoBGP advertises VPC-B's prefixes to VPC-A's VRF on the fabric's RRs
4. Fabric leaves import the routes into VPC-A's VRF, installing forwarding entries

This is standard EVPN inter-VRF route leaking — the product automates what a network engineer would do manually with RT import/export.

### 3.4 Route Reflector Topology

```
                      ┌──────────────┐
                      │   Super RR   │  (inter-region, Tier 2)
                      │ (per DC pair)│
                      └──────┬───────┘
                             │
                ┌────────────┼────────────┐
                │            │            │
          ┌─────┴──────┐ ┌──┴────┐ ┌─────┴──────┐
          │  Spine RR   │ │Spine  │ │  Spine RR   │
          │ (Cluster 1) │ │RR (2) │ │ (Cluster 3) │   Tier 1
          └──────┬──────┘ └──┬────┘ └──────┬──────┘
                 │           │             │
           ┌─────┴───┐  ┌───┴───┐   ┌─────┴───┐
           │ Leaves / │  │Leaves │   │ Leaves / │
           │ BGP Svc  │  │/ BGP  │   │ BGP Svc  │
           └──────────┘  └───────┘   └──────────┘
```

- **Tier 1 (Spine RRs)**: Existing fabric spine switches. 4–8 per pod, serving 48–96 leaves each.
- **Tier 2 (Super RRs)**: Aggregate routes across pods within a region. 2–4 per region. Can be dedicated GoBGP instances for inter-region peering.
- **Tier 3 (Inter-region)**: Super RRs peer across regions for cross-region route distribution.

The `vpc-interconnect-bgp` instances peer with Tier 1 RRs. They do not hold the full global RIB — they only need routes for VPCs they manage (filtered by RT).

### 3.5 Route Filtering and Policy

Routes are filtered at multiple layers:

| Layer | Mechanism | Purpose |
|---|---|---|
| RT-based filtering | VRFs only import routes matching configured RTs | Core isolation (primary) |
| Prefix-list filtering | Outbound filter validates prefixes against VPC's registered CIDRs | Prevent advertising unowned prefixes |
| Community-based policy | Communities mark `peering:transit` vs `peering:direct` | Prefer direct over transit routes |
| Max-prefix limits | Per-VPC cap (default: 100, max: 1,000) | Protect fabric from route table explosion |

GoBGP's `api.AddPolicy` and `api.AddDefinedSet` APIs are used to programmatically install these filters.

### 3.6 Convergence: Sub-1-Second Target

| Mechanism | Timing | Role |
|---|---|---|
| BFD | 3 × 100ms = 300ms detection | Fast failure detection between VTEPs |
| BGP hold timer | 9s keepalive / 3s hold | Backup (BFD triggers immediate teardown) |
| GoBGP graceful restart | Stale timer 60s, restart time 120s | Preserves routes during planned restarts |
| EVPN route withdrawal | Immediate UPDATE with withdrawn routes | Fabric convergence <100ms (hardware FIB) |

End-to-end convergence on unplanned failure: **~300ms** (BFD detection) + **~100ms** (route withdrawal propagation + FIB update) = **~400ms**.

---

## 4. Management Plane / API

### 4.1 API Design

**External API** (customer-facing): REST over HTTPS, versioned (`/v1/`). Implemented via `grpc-gateway` proxying to the internal gRPC service. OpenAPI 3.0 spec auto-generated from protobuf definitions.

**Internal API** (between services): gRPC with mTLS. Protobuf service definitions.

### 4.2 Core API Resources

```
POST   /v1/vpcs                           # Create VPC
GET    /v1/vpcs/{vpc_id}                   # Get VPC details

POST   /v1/peerings                        # Create peering between two VPCs
GET    /v1/peerings/{peering_id}           # Get peering status
DELETE /v1/peerings/{peering_id}           # Delete peering
PATCH  /v1/peerings/{peering_id}           # Update peering (e.g., change policy)
GET    /v1/peerings?vpc_id={vpc_id}        # List peerings for a VPC

POST   /v1/peerings/{peering_id}/accept    # Accept cross-account peering
POST   /v1/peerings/{peering_id}/reject    # Reject cross-account peering

GET    /v1/peerings/{peering_id}/routes    # List effective routes
POST   /v1/peerings/{peering_id}/routes    # Override/filter specific routes

POST   /v1/route-tables                    # Custom route tables
GET    /v1/route-tables/{id}/routes        # List route table entries

POST   /v1/transit-gateways                # Phase 3: create transit hub

GET    /v1/metrics/peerings/{peering_id}   # Per-peering throughput, drops
```

### 4.3 Data Models

```go
type VPC struct {
    ID         uuid.UUID
    AccountID  uuid.UUID
    RegionID   string            // e.g., "us-east-1"
    Name       string
    CIDRBlocks []netip.Prefix    // e.g., [10.0.0.0/16]
    VNI        uint32            // L3 VNI assigned to this VPC
    VRFName    string            // "vpc-<short-uuid>"
    RD         string            // Route Distinguisher
    ExportRT   string            // Export Route Target
    State      VPCState          // active, deleting
    CreatedAt  time.Time
}

type Peering struct {
    ID              uuid.UUID
    AccountID       uuid.UUID
    RequesterVPCID  uuid.UUID
    AccepterVPCID   uuid.UUID
    Direction       PeeringDirection  // bidirectional, requester_to_accepter, accepter_to_requester
    State           PeeringState
    RoutePolicy     RoutePolicy
    CreatedAt       time.Time
    ProvisionedAt   *time.Time
}

type PeeringState string

const (
    PeeringStatePendingAcceptance PeeringState = "pending_acceptance"
    PeeringStateProvisioning      PeeringState = "provisioning"
    PeeringStateActive            PeeringState = "active"
    PeeringStateDegraded          PeeringState = "degraded"
    PeeringStateDeleting          PeeringState = "deleting"
    PeeringStateFailed            PeeringState = "failed"
)

type RoutePolicy struct {
    AllowedPrefixes    []netip.Prefix  // nil = allow all VPC CIDRs
    DeniedPrefixes     []netip.Prefix
    MaxPrefixes        int             // default 100
    BandwidthLimitMbps *int            // nil = unlimited
}

type RouteEntry struct {
    Prefix     netip.Prefix
    NextHop    netip.Addr    // VTEP IP
    VNI        uint32
    PeeringID  uuid.UUID
    Origin     RouteOrigin   // direct, transit, static
    Preference int
    State      RouteState    // active, withdrawn, filtered
}
```

### 4.4 Peering Lifecycle State Machine

```
[User creates peering request]
        │
        ▼
  pending_acceptance
        │
        ├── same-account: auto-accept
        ├── cross-account: accepter calls POST /accept
        │
        ▼
    provisioning
        │
        ├── Controller programs RT import/export on GoBGP
        ├── GoBGP advertises routes to fabric RRs
        ├── Agent programs VRF/iptables on relevant hosts
        ├── Agent confirms routes installed
        │
        ▼
      active ◄──────────── (auto-heal) ──── degraded
        │                                       ▲
        │                                       │
        ├── (failure detected) ─────────────────┘
        │
        ├── (user deletes) ──► deleting ──► (routes withdrawn) ──► deleted
        │
        └── (user updates policy) ──► re-provision routes ──► active
```

The controller uses a **reconciliation loop** (not just event-driven) to continuously ensure desired state matches actual fabric state. This handles missed events, controller restarts, and split-brain recovery.

### 4.5 Multi-Tenancy Isolation

| Layer | Mechanism |
|---|---|
| API | Every request scoped to account via JWT/API-key. Resources keyed by `(account_id, resource_id)` |
| Database | Application-enforced `WHERE account_id = ?` on every query. PostgreSQL row-level security as defense-in-depth |
| Control Plane | RT scoped to account: `target:<account-id>:<vpc-id>`. A management plane bug cannot cause cross-account route leaking because the fabric's RT import/export is the enforcement boundary |
| Cross-account peering | Requires explicit acceptance flow (like AWS VPC peering). Requester creates peering, accepter must call `/accept` |

### 4.6 Database

PostgreSQL is the primary store.

- **Partitioning**: `peerings` table partitioned by `region_id`
- **Key indexes**: composite on `(account_id, vpc_id)` for fast peering lookups
- **Connection pooling**: PgBouncer
- **Migrations**: `golang-migrate`
- **Scale-out path**: CockroachDB for multi-region active-active writes (Phase 2+)

---

## 5. Multi-Region Architecture

### 5.1 Inter-Region Transport

Regions are connected by DCI (Data Center Interconnect) links. The existing fabric runs EVPN Multi-Site with border leaf/gateway nodes.

**Recommended model**: EVPN Multi-Site with border gateways.

| Model | How It Works | Pros | Cons |
|---|---|---|---|
| **EVPN Multi-Site (Border Gateway)** | Border leaves re-originate Type-5 routes with local next-hop | Clean fault isolation per site, natural policy enforcement point | Extra hop at border leaf |
| EVPN Full-Mesh iBGP | All RRs peer across regions, routes carry remote VTEPs | Simpler control plane, fewer hops | Larger RIB on all leaves, wider blast radius |

Border gateways provide fault isolation between regions and give the product a natural point to enforce cross-region policies and metering.

### 5.2 Cross-Region BGP Peering

```
Region A                                    Region B
┌──────────────────────┐                   ┌──────────────────────┐
│  Spine RRs           │                   │  Spine RRs           │
│       ▲              │                   │       ▲              │
│       │              │                   │       │              │
│  BGP Service ◄───────┼─── eBGP/iBGP ────┼──► BGP Service       │
│       │              │   (over DCI)      │       │              │
│  Border Leaf ◄───────┼── VXLAN DCI ─────┼──► Border Leaf       │
└──────────────────────┘                   └──────────────────────┘
```

The `vpc-interconnect-bgp` instances in each region peer with their local RRs. Cross-region route propagation happens through the fabric's existing inter-region BGP/EVPN sessions. The product does **not** create its own inter-region BGP sessions — it publishes routes locally and lets the fabric propagate them.

### 5.3 Latency-Aware Routing

For VPCs peered across multiple regions, the product attaches BGP communities to routes:

- `vpc-interconnect:<region-id>:local` — same-region routes
- `vpc-interconnect:<region-id>:remote` — cross-region routes
- MED set proportional to measured inter-region latency

Leaf switches prefer local routes (lower MED) over remote routes. If the local path fails, the remote path is already in the RIB as a backup.

**Latency measurement**: The `vpc-interconnect-agent` on border leaves measures RTT to peer border leaves using ICMP/BFD every 30 seconds. Measured latency feeds back to the BGP service, which adjusts MED values.

---

## 6. Security Model

### 6.1 Defense in Depth (6 Layers)

| Layer | Mechanism | Enforced By |
|---|---|---|
| L1 | VRF isolation — each VPC in its own VRF, no route leaking by default | Linux kernel + switch ASIC |
| L2 | RT isolation — VRFs only import routes with explicitly configured RTs | GoBGP + fabric switches |
| L3 | VNI isolation — each VPC has a unique VNI; VXLAN decap only delivers to matching VRF | VXLAN datapath |
| L4 | Prefix validation — BGP service validates advertised prefixes against VPC's registered CIDRs | `vpc-interconnect-bgp` outbound filter |
| L5 | API authorization — account-scoped access; cross-account peering requires mutual consent | `vpc-interconnect-api` |
| L6 | ACL at peering boundary — per-peering security rules filter traffic after decap | XDP program or iptables |

### 6.2 ACL Enforcement at Peering Boundaries

When a peering has a `RoutePolicy` with ACL rules, the `vpc-interconnect-agent` programs enforcement at the appropriate tier:

| Tier | Mechanism | When Used |
|---|---|---|
| 0 | iptables/nftables rules in VRF namespace | Default, software kernel path |
| 1 | XDP BPF map entries | High-performance ACL enforcement |
| 2 | Hardware ACL entries on SmartNIC/DPU | Hosts with DPU offload capability |

Rules are expressed as 5-tuple (src prefix, dst prefix, protocol, src port range, dst port range) + action (allow/deny). The agent translates the management plane's `RoutePolicy` into the appropriate enforcement mechanism.

### 6.3 Control Plane Authentication

| Channel | Mechanism |
|---|---|
| BGP sessions (BGP service ↔ fabric RRs) | TCP-AO (RFC 5925), preferred over MD5 (RFC 2385) |
| Internal gRPC (service-to-service) | mTLS with cert rotation via SPIFFE/SPIRE or internal CA |
| External REST API (customer-facing) | HTTPS with JWT (short-lived) or API key. Rate limiting per account |
| Agent-to-controller | mTLS gRPC. Agent authenticates with host-level SPIFFE identity |

### 6.4 Route Hijack Prevention

A malicious or buggy tenant VM must not be able to advertise routes into the fabric:

1. VMs do not run BGP — only the `vpc-interconnect-bgp` service injects routes
2. The BGP service validates every route against the VPC's registered CIDRs before injection
3. Fabric RRs have inbound prefix filters per RR-client session
4. The agent programs reverse-path filtering (RPF) in the VRF to drop packets with source IPs not matching the VPC's CIDRs

---

## 7. Observability

### 7.1 Metrics (Prometheus / OpenTelemetry)

**Per-peering metrics**:

```
vpc_peering_bytes_tx_total{peering_id, direction, region}         counter
vpc_peering_bytes_rx_total{peering_id, direction, region}         counter
vpc_peering_packets_tx_total{peering_id, direction, region}       counter
vpc_peering_packets_dropped_total{peering_id, reason}             counter
    reason: acl_deny | rate_limit | no_route | ttl_exceeded
vpc_peering_state{peering_id}                                     gauge
    0=down, 1=provisioning, 2=active, 3=degraded
```

**Control plane metrics**:

```
vpc_bgp_routes_advertised{vpc_id, route_type}                    gauge
vpc_bgp_routes_received{vpc_id, route_type}                      gauge
vpc_bgp_convergence_seconds{event_type}                           histogram
    event_type: link_failure | peering_create | peering_delete
vpc_bgp_session_state{peer_address}                               gauge
    0=idle, 1=connect, 2=active, 3=opensent, 4=openconfirm, 5=established
vpc_bfd_session_state{peer_address}                               gauge
```

**Management plane metrics**:

```
vpc_api_request_duration_seconds{method, path, status}            histogram
vpc_peering_provisioning_duration_seconds                         histogram
vpc_peerings_total{state}                                         gauge
```

Metrics collected via OpenTelemetry SDK (Go) and exported to Prometheus. The metering daemon reads byte counters from XDP BPF maps or kernel interface stats.

### 7.2 Logging

Structured JSON logging using `slog` (Go 1.21+ stdlib). Key events:

| Event | Level | Notes |
|---|---|---|
| Peering lifecycle transitions | INFO | |
| Route advertisements/withdrawals | DEBUG | |
| BGP session state changes | WARN (down), INFO (established) | |
| ACL deny events | INFO | Sampled at 1/1000 to avoid flood |
| API errors | ERROR | |

Correlation: every peering operation carries a `trace_id` (OpenTelemetry) propagated from API → controller → BGP service → agent.

### 7.3 Distributed Tracing

OpenTelemetry traces span across:
- API request handling
- Controller reconciliation loop (peering provisioning spans)
- BGP route injection (span with route details)
- Agent VRF/interface programming (span per netlink call)

Exported to Jaeger or Grafana Tempo.

### 7.4 Customer-Facing Observability

Available via the API:
- Peering state and health (active, degraded, provisioning)
- Per-peering throughput (bytes in/out — last 5m / 1h / 24h)
- Route count per peering
- Event log (peering created, routes changed, errors)

This data feeds the billing pipeline (cross-region data transfer charges).

---

## 8. Failure Modes & HA

### 8.1 Component HA Strategy

| Component | Strategy | Details |
|---|---|---|
| `vpc-interconnect-api` | Stateless, N+1 replicas behind LB | Any instance can serve any request |
| `vpc-interconnect-controller` | Leader-elected (single active) + warm standby | PostgreSQL advisory locks. On leader failure, standby acquires lock within ~5s |
| `vpc-interconnect-bgp` | N+1 replicas, all peer with RRs | All instances advertise same routes (RRs deduplicate). On failure, surviving instances maintain routes |
| `vpc-interconnect-agent` | Per-host singleton, systemd-managed | Auto-restart. Existing VRF/routes/iptables persist (kernel state is durable). Agent reconciles on restart |

See [ADR-004](#adr-004-controller-leader-election) for leader election choice.

### 8.2 BFD for Fast Failure Detection

BFD sessions established between:
- Host VTEPs (same-region, for host/link failure detection)
- Border leaf VTEPs (cross-region, for DCI failure detection)

Timers: Tx 100ms, Rx 100ms, detect multiplier 3. **Failure detected in 300ms.**

BFD state changes trigger:
1. Local BGP session teardown → immediate route withdrawal
2. Agent reprograms affected routes (remove failed next-hop, activate backup if available)

**BFD integration**: Use FRR's `bfdd` daemon alongside `vpc-interconnect-bgp`. BFD state changes communicated via Unix socket or shared state. See [ADR-006](#adr-006-bfd-implementation).

### 8.3 Graceful Restart

GoBGP supports BGP Graceful Restart (RFC 4724):

1. Peer RRs retain routes with stale flag for the restart time (120s)
2. BGP service restarts, re-establishes sessions, sends End-of-RIB
3. Stale routes not refreshed are purged after stale timer (60s)

During this window, **forwarding continues uninterrupted** because the fabric's FIB entries are not withdrawn.

### 8.4 Split-Brain Handling

If the controller loses inter-region connectivity:

- Each region's controller continues to operate independently (local peerings unaffected)
- Cross-region peerings enter `degraded` state (data path / DCI is also likely down)
- On connectivity restore, controllers reconcile state
- A **generation counter** on each peering record prevents stale controller instances from overwriting newer state
- Database conflict resolution: last-writer-wins on peering state, append-only for route entries

---

## 9. Scale Design

### 9.1 Scale Targets

| Dimension | Target | Limiting Factor |
|---|---|---|
| VPCs per account | 1,000 | RT space (32-bit), manageable |
| Peerings per VPC | 125 | Fabric RIB size per VRF |
| Total peerings per region | 100,000 | Controller DB throughput, BGP service capacity |
| Routes per VPC | 100 (default), 1,000 (max) | Leaf TCAM (128K–256K entries) |
| Total routes in fabric | ~1,000,000 | Route reflector memory (~8 GB for 1M EVPN routes) |
| Regions | 16 | VNI bit allocation (4 bits), expandable |

### 9.2 Avoiding O(N^2) Scaling

If every VPC peers with every other VPC, peering count is O(N^2). With 1,000 VPCs, that's 500,000 peerings.

**Mitigations**:

1. **Transit gateway (Phase 3)**: A hub VPC that acts as a transit point. N VPCs peer with 1 hub = N peerings, not N^2. Implemented as a VRF with all tenants' RTs imported.
2. **RT aggregation**: Shared RTs for groups of VPCs needing full mesh connectivity (e.g., `target:account-123:mesh-group-1`).
3. **Route summarization**: Advertise aggregate prefixes (e.g., 10.0.0.0/8) instead of individual /24s where possible.

### 9.3 VNI Space Management

Central allocator service backed by database:

- Table: `(vni, vpc_id, account_id, region_id)` with unique constraint on `vni`
- De-allocation: soft-delete with **7-day grace period** (prevents VNI reuse race conditions in the fabric)
- Reserved ranges: VNI 1–4095 (overlaps VLAN IDs), 16,000,000+ (future use)

### 9.4 Route Reflector Hierarchy

For 1,000s of VPCs generating up to 1M routes:

| Tier | Role | Count | Scope |
|---|---|---|---|
| 1 (Spine RRs) | Serve leaf switches per pod | 4–8 per pod | 48–96 leaves each |
| 2 (Super RRs) | Aggregate across pods | 2–4 per region | Regional |
| 3 (Inter-region) | Cross-region distribution | 2 per region pair | Global |

The `vpc-interconnect-bgp` instances peer with Tier 1 RRs and only hold routes for VPCs they manage (RT-filtered).

### 9.5 Database Scaling

| Technique | Phase |
|---|---|
| Partitioning (`peerings` by `region_id`) | Phase 1 |
| Composite index on `(account_id, vpc_id)` | Phase 1 |
| PgBouncer connection pooling | Phase 1 |
| Read replicas for API reads and metrics | Phase 2 |
| CockroachDB for multi-region active-active | Phase 3 (if needed) |

---

## 10. Implementation Roadmap

### Phase 1: Single-Region Peering (12–16 weeks)

**Goal**: VPC-A to VPC-B peering within one region. Core product, end-to-end.

| Weeks | Focus | Deliverables |
|---|---|---|
| 1–3 | Foundation | Protobuf schemas, `vpc-interconnect-api` (REST + gRPC), PostgreSQL schema + migrations, CI/CD |
| 4–7 | Control Plane | `vpc-interconnect-bgp` embedding GoBGP, Type-5 route construction, RT management, BGP session auth |
| 8–10 | Data Plane Agent | `vpc-interconnect-agent` with netlink (VRF, VXLAN, nftables), agent-controller gRPC, reconciliation loop |
| 11–13 | Integration | End-to-end peering flow, failure testing, performance testing, basic metrics + logging |
| 14–16 | Hardening | Controller leader election, graceful restart, API rate limiting, documentation, runbooks |

**Exit criteria**: Customers can create/delete VPC peerings within a region via API. Traffic flows at line rate via fabric. Sub-1s convergence on failures.

### Phase 2: Multi-Region + Observability + Metering (10–14 weeks)

**Goal**: Cross-region peering, customer-facing metrics, billing-grade metering.

| Weeks | Focus | Deliverables |
|---|---|---|
| 1–4 | Multi-Region Control Plane | Cross-region route propagation, MED-based routing, BFD integration, inter-region peering state |
| 5–8 | Metering & Observability | `vpc-interconnect-meter` (XDP counters), OTel pipeline, customer metrics API, distributed tracing, billing events (Kafka/NATS) |
| 9–12 | XDP Fast Path | XDP ACL program (`cilium/ebpf`), BPF map management, performance benchmarking vs iptables |
| 13–14 | Hardening | Cross-region failure scenarios, metering accuracy validation, load testing (100+ VPCs, 1000+ peerings) |

**Exit criteria**: Cross-region VPC peering. Per-peering metrics visible to customers. Billing-grade metering. XDP-accelerated ACLs.

### Phase 3: Transit Gateway + Hub-Spoke (8–12 weeks)

**Goal**: Transit gateway for hub-and-spoke topologies.

| Weeks | Focus | Deliverables |
|---|---|---|
| 1–4 | Transit Gateway Core | `TransitGateway` data model + API, transit VRF implementation, route propagation rules |
| 5–8 | Advanced Topologies | Hub-spoke with route filtering, shared services VPC, multi-region transit gateway |
| 9–12 | DPU Offload + Ecosystem | SmartNIC/DPU integration, API docs, Go/Python/TypeScript SDKs, Terraform provider |

**Exit criteria**: Transit gateway product. Hub-spoke topologies. Terraform provider. DPU offload.

---

## Appendix A: Architecture Decision Records

### ADR-001: Fabric-Native Route Leaking vs. Overlay-on-Overlay

**Decision**: Fabric-native. Inter-VPC routes injected directly into the EVPN-VXLAN fabric as Type-5 routes.

**Context**: Two approaches exist — (a) inject routes into the existing fabric, or (b) build a separate overlay (WireGuard/IPsec tunnels between hypervisors) independent of the fabric.

**Rationale**: We own the fabric. Fabric-native gives line-rate forwarding via switch ASICs, zero additional encapsulation overhead, and leverages existing ECMP, BFD, and graceful restart. An overlay-on-overlay adds latency, MTU tax (double encap), and a parallel failure domain to operate.

**Trade-off**: Couples the product to the fabric vendor's EVPN implementation. Acceptable because we control the fabric.

### ADR-002: Embedded GoBGP vs. FRR for EVPN Route Injection

**Decision**: Embedded GoBGP as a Go library.

**Context**: Two BGP stacks are viable — GoBGP (Go-native, embeddable) and FRR (C, mature, CLI-driven).

**Rationale**: GoBGP provides programmatic control via in-process API calls (no IPC, no CLI parsing). It supports EVPN Type-5 routes, route policies, and graceful restart. Embedding it avoids managing a separate daemon and its configuration file.

**Trade-off**: GoBGP is less battle-tested than FRR in large-scale production EVPN deployments. Mitigated by thorough integration testing and the ability to swap to FRR later if needed.

### ADR-003: RT-Based Isolation vs. VNI-per-Peering

**Decision**: RT-based isolation using import/export route targets.

**Context**: Two isolation models — (a) assign a unique VNI to each peering, or (b) use RT import/export to leak routes between existing VPC VRFs.

**Rationale**: VNI-per-peering would exhaust the 24-bit VNI space at scale (100K peerings = 100K VNIs consumed). RT-based isolation reuses existing VPC VNIs and is the standard EVPN mechanism for inter-VRF connectivity.

### ADR-004: Controller Leader Election

**Decision**: PostgreSQL advisory locks (Phase 1). Evaluate etcd if failover time becomes critical.

**Context**: The controller must be single-active to prevent conflicting reconciliation. Options: PostgreSQL advisory locks (no new dependency) vs. etcd (faster failover, new dependency).

**Rationale**: PostgreSQL advisory locks avoid adding etcd as a dependency. Failover time is ~5s (lock expiry). Acceptable for Phase 1 where control plane changes are infrequent. If sub-second failover becomes required, add etcd.

### ADR-005: XDP vs. DPDK for ACL Enforcement

**Decision**: XDP for Phase 2. DPDK as Phase 3 option.

**Context**: ACL enforcement needs a fast path. Options: XDP (eBPF, kernel-integrated) vs. DPDK (userspace, kernel-bypass).

**Rationale**: XDP is simpler to deploy (no kernel bypass, no hugepage config, no dedicated cores), integrates with existing kernel networking, and provides 10–40 Gbps/core — sufficient for most workloads. DPDK provides higher throughput but requires dedicated infrastructure. Reserve for Phase 3 extreme-throughput scenarios.

### ADR-006: BFD Implementation

**Decision**: FRR `bfdd` sidecar (recommended for production). `go-bfd` library acceptable for initial development.

**Context**: GoBGP does not natively run BFD. Options: FRR's `bfdd` daemon (battle-tested, C) vs. `go-bfd` library (Go-native, less mature).

**Rationale**: FRR's `bfdd` is production-proven and handles edge cases (e.g., timer negotiation, echo mode). Communication with the Go BGP service via Unix socket or shared state file. `go-bfd` is acceptable for development/testing but should be replaced for production.

---

## Appendix B: Go Dependencies

| Library | Import Path | Purpose |
|---|---|---|
| GoBGP v3 | `github.com/osrg/gobgp/v3` | BGP/EVPN route injection and consumption |
| vishvananda/netlink | `github.com/vishvananda/netlink` | Linux VRF, VXLAN, route, and iptables programming |
| cilium/ebpf | `github.com/cilium/ebpf` | XDP program loading and BPF map management |
| grpc-go | `google.golang.org/grpc` | Internal gRPC APIs |
| grpc-gateway v2 | `github.com/grpc-ecosystem/grpc-gateway/v2` | REST-to-gRPC proxy for customer API |
| pgx v5 | `github.com/jackc/pgx/v5` | PostgreSQL driver |
| golang-migrate v4 | `github.com/golang-migrate/migrate/v4` | Database schema migrations |
| OpenTelemetry Go | `go.opentelemetry.io/otel` | Metrics, traces, and logs |
| slog | `log/slog` (stdlib) | Structured JSON logging |
| google/uuid | `github.com/google/uuid` | UUID generation |
