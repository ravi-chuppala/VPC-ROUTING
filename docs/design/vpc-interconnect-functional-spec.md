# VPC Interconnect — Functional Specification

**Status**: Draft  
**Date**: 2026-04-06  
**Related**: [Technical Architecture](vpc-interconnect-architecture.md)

---

## Table of Contents

1. [Purpose](#1-purpose)
2. [Definitions](#2-definitions)
3. [User Personas](#3-user-personas)
4. [Functional Requirements](#4-functional-requirements)
   - FR-1: VPC Management
   - FR-2: VPC Peering Lifecycle
   - FR-3: Cross-Account Peering
   - FR-4: Route Management
   - FR-5: Route Policy and Filtering
   - FR-6: Multi-Region Peering
   - FR-7: Transit Gateway (Phase 3)
   - FR-8: Observability and Metrics
   - FR-9: Metering and Billing
   - FR-10: Security and Access Control
5. [Non-Functional Requirements](#5-non-functional-requirements)
6. [API Specification](#6-api-specification)
7. [User Flows](#7-user-flows)
8. [Error Handling](#8-error-handling)
9. [Quotas and Limits](#9-quotas-and-limits)
10. [Phased Delivery](#10-phased-delivery)

---

## 1. Purpose

This document defines the functional behavior of the VPC Interconnect product — what it does from the customer's perspective. It specifies every user-facing operation, its inputs, outputs, side effects, error conditions, and constraints.

The companion [Technical Architecture](vpc-interconnect-architecture.md) describes **how** the system is built. This spec describes **what** it does and **when**.

### 1.1 Product Goal

Enable customers to create private, high-performance network connectivity between any two VPCs on the platform — within a region or across regions — through a simple API call. The system handles route exchange, encapsulation, policy enforcement, and failover transparently.

### 1.2 Competitive Positioning

| Competitor | Equivalent Products |
|---|---|
| AWS | VPC Peering + Transit Gateway |
| GCP | VPC Network Peering + Cloud Router |
| Azure | VNet Peering + Virtual WAN |

---

## 2. Definitions

| Term | Definition |
|---|---|
| **VPC** | Virtual Private Cloud. An isolated network environment belonging to a customer account, identified by one or more CIDR blocks |
| **Peering** | A network connection between two VPCs that enables private IP traffic to flow between them |
| **Route** | A network prefix (e.g., 10.2.0.0/16) reachable through a peering connection |
| **Route Table** | An ordered set of route entries that determines how traffic is forwarded from a VPC |
| **Route Policy** | A set of rules (allowed/denied prefixes, max-prefix limit, bandwidth cap) applied to a peering |
| **Transit Gateway** | A hub resource that enables hub-and-spoke or many-to-many connectivity without O(N^2) peering (Phase 3) |
| **Region** | A geographic deployment of the cloud infrastructure (e.g., `us-east-1`, `eu-west-1`) |
| **Account** | A customer's billing and access-control boundary |
| **CIDR** | Classless Inter-Domain Routing notation for IP address ranges (e.g., 10.0.0.0/16) |
| **Requester** | The account/VPC that initiates a peering request |
| **Accepter** | The account/VPC that receives and must approve a cross-account peering request |

---

## 3. User Personas

### 3.1 Platform Engineer

Manages cloud infrastructure for their organization. Creates VPCs, sets up peerings for production workloads (multi-cluster Kubernetes, geo-distributed databases, shared services). Interacts via Terraform provider or CLI. Cares about reliability, convergence speed, and operational simplicity.

### 3.2 Network Engineer

Responsible for connectivity and security policy. Configures route policies, ACLs, and bandwidth limits on peerings. Monitors route tables and convergence metrics. Needs granular control over which prefixes are exchanged.

### 3.3 Application Developer

Consumes VPC peering as infrastructure. Expects that once a peering is active, connectivity between VPCs "just works" at the IP layer. Does not configure peering directly but relies on it being in place.

### 3.4 FinOps / Billing Administrator

Monitors cross-region data transfer costs. Needs per-peering throughput visibility and billing-grade metering data.

---

## 4. Functional Requirements

### FR-1: VPC Management

> **Scope**: VPC registration for the interconnect system. VPC lifecycle (create/destroy the VPC itself) may be managed by an external platform. This system needs VPCs registered so it can manage their peering connectivity.

#### FR-1.1: Register a VPC

**Trigger**: Customer calls `POST /v1/vpcs`

**Input**:

| Field | Type | Required | Constraints |
|---|---|---|---|
| `name` | string | Yes | 1–128 characters, unique per account+region |
| `region_id` | string | Yes | Must be a valid, active region |
| `cidr_blocks` | array of CIDR strings | Yes | 1–5 CIDRs. Each must be RFC 1918 private range. No overlap with other VPCs in the same account |

**Behavior**:
1. Validate input (see constraints above)
2. Check CIDR overlap: reject if any provided CIDR overlaps with any existing VPC CIDR in the same account (across all regions)
3. Allocate a VNI from the VNI pool for the target region
4. Assign a Route Distinguisher (`<region-id>:<vpc-sequence>`) and Export Route Target (`target:<account-id>:<vpc-id>`)
5. Create VRF name: `vpc-<first-8-chars-of-uuid>`
6. Persist VPC record with `state: active`
7. Return VPC object with all allocated identifiers

**Output**: VPC object (see [Section 6.2](#62-resource-schemas))

**Errors**:

| Code | Condition |
|---|---|
| 400 | Invalid CIDR format, non-RFC 1918 range, or name too long |
| 409 | CIDR overlaps with existing VPC in the same account |
| 409 | Name already exists in this account+region |
| 422 | VNI pool exhausted for target region |
| 429 | Rate limit exceeded |

#### FR-1.2: Get VPC

**Trigger**: `GET /v1/vpcs/{vpc_id}`

**Behavior**: Return VPC details including CIDR blocks, VNI, region, state, and a count of active peerings.

**Authorization**: Caller must own the VPC (same account).

#### FR-1.3: List VPCs

**Trigger**: `GET /v1/vpcs`

**Query Parameters**:

| Param | Type | Default | Description |
|---|---|---|---|
| `region_id` | string | (all) | Filter by region |
| `page_token` | string | — | Pagination cursor |
| `page_size` | int | 50 | 1–200 |

**Behavior**: Return paginated list of VPCs owned by the caller's account, sorted by creation time descending.

#### FR-1.4: Delete VPC

**Trigger**: `DELETE /v1/vpcs/{vpc_id}`

**Preconditions**: VPC must have zero peerings in `active`, `pending_acceptance`, `provisioning`, `pending_deletion`, or `disabled` states. If such peerings exist, return 409 with a list of blocking peering IDs and their states. Peerings in `failed`, `expired`, `rejected`, or `deleted` states do not block VPC deletion.

**Behavior**:
1. Set VPC state to `deleting`
2. De-allocate VNI (soft-delete with 7-day grace period)
3. Remove VRF configuration from all hosts via agent
4. Once cleanup confirms, set state to `deleted` (tombstone retained for 30 days for audit)

---

### FR-2: VPC Peering Lifecycle

#### FR-2.1: Create Peering

**Trigger**: `POST /v1/peerings`

**Input**:

| Field | Type | Required | Constraints |
|---|---|---|---|
| `requester_vpc_id` | UUID | Yes | Must be an active VPC owned by the caller |
| `accepter_vpc_id` | UUID | Yes | Must be an active VPC. May be owned by a different account (see FR-3) |
| `direction` | enum | No | `bidirectional` (default), `requester_to_accepter`, `accepter_to_requester` |
| `route_policy` | object | No | See FR-5. Defaults to allow-all with max_prefixes=100 |

**Behavior**:

1. **Validate ownership**: Caller must own `requester_vpc_id`
2. **CIDR overlap check**: Reject if the two VPCs have overlapping CIDRs (peering with overlapping address space is undefined behavior)
3. **Duplicate check**: Reject if an active or pending peering already exists between these two VPCs (regardless of direction)
4. **Quota check**: Reject if either VPC has reached its peering limit (default: 125)
5. **Same-account peering**: If both VPCs belong to the caller's account, auto-accept. Set state to `provisioning` immediately.
6. **Cross-account peering**: If `accepter_vpc_id` belongs to a different account, set state to `pending_acceptance`. The accepter must call `/accept` (see FR-3).
7. **Provisioning** (when accepted):
   - Controller instructs BGP service to configure RT import/export between the two VPCs
   - BGP service injects EVPN Type-5 routes for each VPC's CIDRs into the peer VPC's VRF
   - Agent programs VRF route entries and optional ACL rules on relevant hosts
   - Agent reports route installation confirmation
   - State transitions to `active`
8. **Provisioning timeout**: If provisioning does not complete within 5 minutes, state transitions to `failed` with a diagnostic reason

**Output**: Peering object with state reflecting the outcome

**Errors**:

| Code | Condition |
|---|---|
| 400 | Missing required fields, invalid UUID format |
| 403 | Caller does not own `requester_vpc_id` |
| 404 | Either VPC does not exist |
| 409 | CIDR overlap between the two VPCs |
| 409 | Peering already exists between these VPCs |
| 422 | Peering limit reached for either VPC |
| 429 | Rate limit exceeded |

#### FR-2.2: Get Peering

**Trigger**: `GET /v1/peerings/{peering_id}`

**Behavior**: Return peering details including state, both VPC IDs, route policy, provisioned timestamp, and health status.

**Authorization**: Caller must own either the requester or accepter VPC.

**Health Status** (computed field):

| Health | Condition |
|---|---|
| `healthy` | State is `active`, all routes installed, BFD sessions up |
| `degraded` | State is `active` but some routes missing or BFD session(s) down |
| `down` | State is not `active` |

#### FR-2.3: List Peerings

**Trigger**: `GET /v1/peerings`

**Query Parameters**:

| Param | Type | Default | Description |
|---|---|---|---|
| `vpc_id` | UUID | (all) | Filter peerings involving this VPC |
| `state` | enum | (all) | Filter by state |
| `page_token` | string | — | Pagination cursor |
| `page_size` | int | 50 | 1–200 |

**Behavior**: Return peerings where the caller's account owns at least one side. Sorted by creation time descending.

#### FR-2.4: Update Peering

**Trigger**: `PATCH /v1/peerings/{peering_id}`

**Input**: Partial update. Only `route_policy` and `direction` fields are mutable.

**Authorization**: Caller must own the requester VPC. Minimum role: `operator`. For cross-account peerings, **only the requester** can update policy and direction. The accepter cannot modify the peering — they can only delete/disable it or override routes on their own VPC (see FR-4.2). This prevents the accepter from widening the requester's route exposure without consent.

**Behavior**:
1. Validate caller owns the requester VPC and has `operator` or `admin` role
2. Validate the update is semantically valid:
   - `route_policy` changes are validated against FR-5.1 schema constraints
   - `direction` changes cannot widen access without the other side's existing routes being compatible
3. Apply new route policy: controller re-evaluates prefix filters, updates BGP policies, agent reprograms ACL rules
4. If direction changes (e.g., bidirectional → requester_to_accepter), withdraw routes for the removed direction
5. State remains `active` throughout (no re-provisioning needed for policy-only changes)
6. If route policy change requires route withdrawal/advertisement, BGP service processes updates. Effective within 5 seconds (subject to coalescing, see FR-5.3).

**Direction change safeguards**:
- Changing direction on a cross-account peering emits a `direction_changed` event visible to both sides
- Changing from a narrower direction to `bidirectional` is a privilege escalation that exposes the accepter's routes to the requester. An audit event is emitted.

**Errors**:

| Code | Condition |
|---|---|
| 400 | Attempting to modify immutable fields (`requester_vpc_id`, `accepter_vpc_id`) |
| 400 | Invalid route policy (e.g., `max_prefixes` < 0, malformed CIDR in allowed/denied list) |
| 403 | Caller does not own the requester VPC |
| 403 | Caller has insufficient role (requires `operator` or `admin`) |
| 409 | Peering is not in `active` state |
| 429 | Policy update rate limit exceeded (10/minute per peering) |

#### FR-2.5: Delete Peering

**Trigger**: `DELETE /v1/peerings/{peering_id}`

**Authorization**: Caller must own either the requester or accepter VPC. Either side can initiate deletion.

**Same-account peering** (both VPCs owned by same account):
- Deletion is **immediate**. State transitions to `deleting` and routes are withdrawn.

**Cross-account peering** (VPCs owned by different accounts):
- Deletion follows a **grace period model** to prevent surprise traffic disruption:
  1. Initiator calls `DELETE` → state transitions to `pending_deletion` (not `deleting`)
  2. The peer account receives a `peering_pending_deletion` event (visible in event log)
  3. A **24-hour grace period** begins. During this period:
     - Traffic continues to flow (routes remain active)
     - The peering response includes `deletion_scheduled_at` timestamp
     - The initiator can **cancel** deletion by calling `POST /v1/peerings/{peering_id}/cancel-deletion`
     - The peer can **acknowledge** early deletion by calling `DELETE /v1/peerings/{peering_id}` (both sides agree → immediate deletion)
  4. After 24 hours, state transitions to `deleting` and routes are withdrawn
- **Emergency override**: Include `"force": true` in the request body to skip the grace period. This is logged as an audit event (`peering_force_deleted`) and emits a `peering_deleted` event to the peer immediately. Intended for incident response only.

**Behavior** (once state reaches `deleting`):
1. Controller instructs BGP service to remove RT import/export and withdraw all routes for this peering
2. Agent removes ACL rules and cleans up VRF route entries on relevant hosts
3. BGP route withdrawal propagates to fabric (sub-1-second)
4. Traffic between the two VPCs stops flowing
5. State transitions to `deleted` (tombstone retained for 30 days)

**Errors**:

| Code | Condition |
|---|---|
| 403 | Caller does not own either VPC |
| 409 | Peering is already in `deleting` or `deleted` state |
| 409 | Peering is in `pending_deletion` and caller is the initiator (use cancel-deletion instead) |

#### FR-2.6: Cancel Pending Deletion

**Trigger**: `POST /v1/peerings/{peering_id}/cancel-deletion`

**Precondition**: Peering state must be `pending_deletion`. Caller must be the account that initiated the deletion.

**Behavior**: State transitions back to `active`. Grace period timer is cancelled. A `deletion_cancelled` event is emitted.

#### FR-2.7: Disable Peering

**Trigger**: `POST /v1/peerings/{peering_id}/disable`

**Authorization**: Caller must own either the requester or accepter VPC.

**Behavior**: A non-destructive alternative to deletion. Routes are withdrawn but the peering configuration is preserved. State transitions to `disabled`. To re-enable, call `POST /v1/peerings/{peering_id}/enable`, which re-provisions routes (same flow as initial provisioning). This avoids the need to recreate the peering and re-negotiate cross-account acceptance.

**Note**: Customers who want to temporarily stop traffic should prefer `disable` over deleting and recreating the peering. Deletion is permanent. Route policy `denied_prefixes: ["0.0.0.0/0"]` is another option for fine-grained traffic control without state change.

---

### FR-3: Cross-Account Peering

#### FR-3.1: Accept Peering

**Trigger**: `POST /v1/peerings/{peering_id}/accept`

**Precondition**: Peering state must be `pending_acceptance`. Caller must own the accepter VPC.

**Behavior**:
1. Validate caller owns the accepter VPC
2. Transition state to `provisioning`
3. Proceed with provisioning flow (same as FR-2.1 step 7)

**Timeout**: Pending peerings that are not accepted within 7 days are automatically expired (state → `expired`).

#### FR-3.2: Reject Peering

**Trigger**: `POST /v1/peerings/{peering_id}/reject`

**Precondition**: Peering state must be `pending_acceptance`. Caller must own the accepter VPC.

**Behavior**: Transition state to `rejected`. No routes are configured. The requester sees the peering as `rejected`.

#### FR-3.3: Cross-Account Visibility

- The **requester** can see the full peering object including both VPC IDs, but can only see the accepter VPC's ID and region (not its name, CIDRs, or other details)
- The **accepter** can see the full peering object and the requester VPC's ID and region
- Neither side can see the other account's VPC details through the peering API — they must have direct access to that account

---

### FR-4: Route Management

#### FR-4.1: List Effective Routes

**Trigger**: `GET /v1/peerings/{peering_id}/routes`

**Authorization**: Caller must own either the requester or accepter VPC. Minimum role: `viewer`.

**Behavior**: Return the set of routes currently installed for this peering. Each route entry includes:

| Field | Type | Description |
|---|---|---|
| `prefix` | CIDR | The IP prefix (e.g., 10.2.0.0/16) |
| `origin_vpc_id` | UUID | The VPC that originates this prefix |
| `state` | enum | `active`, `withdrawn`, `filtered` |
| `origin` | enum | `direct` (from VPC CIDR), `static` (manually added), `transit` (via transit gateway) |
| `next_hop` | string | VTEP IP (informational, for debugging) |
| `filtered_reason` | string or null | If `state: filtered`, the reason: `denied_prefix`, `not_in_allowed`, `max_prefix_exceeded` |

**Filtered route visibility**: Routes with `state: filtered` are present in the route table but blocked by route policy. They are included so the customer can understand what would be reachable if the policy were changed.

**Per-side visibility rules for filtered routes**:
- Each side of the peering sees filtered routes **from the peer's VPC** (routes the peer is trying to send but that are blocked by the local side's policy). This is the useful view: "what is my policy blocking?"
- Each side does **not** see routes from their own VPC that are filtered by the peer's policy. To see those, query the routes from the peer side (requires cross-account access to the peer's account, or the peer shares this information out-of-band).
- In a same-account peering, the caller sees all filtered routes from both directions since they own both sides.

**Cross-account route visibility**:
- Routes include `origin_vpc_id` but NOT the peer VPC's name, CIDRs, or other details (consistent with FR-3.3)
- `next_hop` (VTEP IP) is an infrastructure-internal address. For cross-account peerings, this field is **redacted** (set to `null`) to prevent leaking infrastructure topology to external accounts.

#### FR-4.2: Override Routes

**Trigger**: `POST /v1/peerings/{peering_id}/routes`

**Authorization**: Caller must own either the requester or accepter VPC. The caller can only modify routes **originating from their own VPC** — they cannot inject or withdraw routes on behalf of the peer's VPC.

**Input**:

| Field | Type | Required | Constraints |
|---|---|---|---|
| `action` | enum: `add_static`, `withdraw` | Yes | |
| `prefix` | CIDR | Yes | Must be a valid RFC 1918 prefix, /16–/28 |

**Behavior**:
- `add_static`: Inject a more-specific static route into the peering's route table. The prefix must fall within one of the **caller's own VPC's** CIDR blocks (no arbitrary prefix injection, no injecting routes for the peer's address space). The caller's VPC is determined by which side of the peering the caller owns. Useful for more-specific routing (e.g., advertising 10.0.1.0/24 when the VPC CIDR is 10.0.0.0/16).
- `withdraw`: Withdraw a specific prefix from the peering. The caller can only withdraw prefixes originating from **their own VPC**. Withdrawing a prefix means the peer VPC will no longer have a route to that prefix through this peering. This is a voluntary reachability reduction, not a way to block the peer's routes (use `denied_prefixes` in route policy for that).

**Constraints**:
- A caller who owns both sides of a same-account peering must specify `origin_vpc_id` in the request to disambiguate which VPC's routes they are modifying.
- Static routes cannot be broader than the VPC's registered CIDRs (no supernet injection).
- Static routes cannot duplicate an existing direct route (the VPC's own CIDR) — they must be more-specific.
- Rate limit: route override operations are classified as **mutating** (60/min per account).

**Errors**:

| Code | Condition |
|---|---|
| 400 | Prefix not within caller's VPC's CIDR blocks |
| 400 | Prefix is broader than or equal to VPC's registered CIDR (must be more-specific) |
| 400 | Same-account peering: `origin_vpc_id` not specified or doesn't match either VPC |
| 403 | Caller does not own either VPC in the peering |
| 403 | Attempting to add/withdraw a prefix for the peer's VPC (not caller's) |
| 409 | Static route already exists for this prefix on this peering |
| 422 | Static route limit (50 per peering) exceeded |

---

### FR-5: Route Policy and Filtering

Route policies control which prefixes are exchanged and enforce traffic limits per peering.

#### FR-5.1: Route Policy Schema

```json
{
  "allowed_prefixes": ["10.2.0.0/24", "10.2.1.0/24"],
  "denied_prefixes": ["10.2.255.0/24"],
  "max_prefixes": 100,
  "bandwidth_limit_mbps": 1000
}
```

| Field | Type | Default | Description |
|---|---|---|---|
| `allowed_prefixes` | array of CIDR | `null` (allow all) | If set, **only** these prefixes are exchanged. Whitelist mode |
| `denied_prefixes` | array of CIDR | `[]` | These prefixes are never exchanged, even if in `allowed_prefixes` |
| `max_prefixes` | int | 100 | Maximum number of prefixes accepted from the peer VPC. If exceeded, new prefixes are rejected and an alert is raised |
| `bandwidth_limit_mbps` | int or null | `null` (unlimited) | Per-peering bandwidth cap. Enforced at the data plane (XDP/tc). Traffic exceeding the limit is dropped |

#### FR-5.2: Policy Evaluation Order

1. Check `denied_prefixes` — if the prefix matches any denied entry, **reject**
2. Check `allowed_prefixes` — if the list is non-null and the prefix does not match any entry, **reject**
3. Check `max_prefixes` — if the total accepted prefix count would exceed the limit, **reject** and raise alert
4. **Accept** the prefix

#### FR-5.3: Policy Update Behavior

When a route policy is updated on an active peering:
- Newly denied prefixes are withdrawn within 5 seconds
- Newly allowed prefixes are advertised within 5 seconds
- Bandwidth limit changes take effect within 10 seconds (agent reprograms XDP/tc)
- No traffic disruption for prefixes that remain allowed

**Churn protection**: Policy updates are rate-limited to **10 per minute per peering** (see FR-10.4). If a policy is updated multiple times within a short window, the controller **coalesces** updates: it waits 2 seconds after the last policy write before triggering BGP re-evaluation, ensuring that rapid successive changes (e.g., Terraform apply with multiple policy fields) result in a single BGP update cycle rather than one per field.

**Policy update idempotency**: If the new policy is identical to the current policy (no effective change), the API returns `200 OK` but does NOT trigger a BGP re-evaluation. The controller compares policy hashes before acting.

#### FR-5.4: Max-Prefix Violation

When a peer VPC attempts to advertise more prefixes than `max_prefixes`:
1. Excess prefixes are **not installed** (they remain in `filtered` state)
2. An event is emitted: `max_prefix_limit_reached` (visible in peering event log)
3. The peering remains `active` — existing routes are not affected
4. The customer can increase `max_prefixes` via `PATCH /v1/peerings/{id}` to accept the additional routes

---

### FR-6: Multi-Region Peering

#### FR-6.1: Cross-Region Peering Creation

Cross-region peering uses the same API as same-region peering (`POST /v1/peerings`). The system detects that the two VPCs are in different regions and automatically configures cross-region route propagation.

**Additional behavior for cross-region peering**:
- Routes are propagated through border gateway / DCI fabric links
- BGP MED values are set based on measured inter-region latency (lower MED = preferred path)
- Cross-region peerings have a `cross_region: true` flag in the peering response

#### FR-6.2: Latency Visibility

**Trigger**: `GET /v1/peerings/{peering_id}`

For cross-region peerings, the response includes:

| Field | Type | Description |
|---|---|---|
| `cross_region` | bool | `true` if VPCs are in different regions |
| `measured_latency_ms` | float | Current measured RTT between regions (updated every 30s) |
| `regions` | object | `{ "requester": "us-east-1", "accepter": "eu-west-1" }` |

#### FR-6.3: Region Failover

If inter-region connectivity is lost (DCI failure):
1. Cross-region peerings transition to `degraded` state
2. Routes for the remote region are marked as `withdrawn`
3. An event is emitted: `cross_region_connectivity_lost`
4. When connectivity restores, routes are re-advertised and peering returns to `active`
5. State transitions happen automatically — no customer action required

---

### FR-7: Transit Gateway (Phase 3)

> Phase 3 feature. Included for completeness.

#### FR-7.1: Create Transit Gateway

**Trigger**: `POST /v1/transit-gateways`

**Input**:

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | Yes | Transit gateway name |
| `region_id` | string | Yes | Home region |
| `asn` | int | No | Private ASN for the transit gateway (auto-assigned if omitted) |

**Behavior**: Creates a transit hub. VPCs attach to the transit gateway instead of peering directly with each other. N VPCs attach to 1 transit gateway = N attachments (not N^2 peerings).

#### FR-7.2: Attach VPC to Transit Gateway

**Trigger**: `POST /v1/transit-gateways/{tgw_id}/attachments`

**Input**: `{ "vpc_id": "...", "route_propagation": "full" | "none" }`

**Behavior**:
- `full`: All VPC routes are propagated to the transit gateway's route table and redistributed to other attached VPCs
- `none`: Attachment is created but no routes are exchanged. Routes must be added manually to the transit gateway route table.

#### FR-7.3: Transit Gateway Route Table

**Trigger**: `GET /v1/transit-gateways/{tgw_id}/route-table`

Returns the consolidated route table showing all prefixes from all attached VPCs, with next-hop pointing to the originating VPC's attachment.

#### FR-7.4: Spoke Isolation

By default, spokes (VPCs attached to the transit gateway) **can** see each other's routes. To create a hub-and-spoke topology where spokes cannot communicate directly:
- Create separate route table associations
- Use route policy on each attachment to allow only hub ↔ spoke traffic

---

### FR-8: Observability and Metrics

#### FR-8.1: Peering Metrics

**Trigger**: `GET /v1/metrics/peerings/{peering_id}`

**Authorization**: Caller must own either the requester or accepter VPC. Minimum role: `viewer`. For cross-account peerings, each side sees only their own directional metrics (ingress/egress relative to their VPC). Neither side sees the other account's per-VPC breakdowns.

**Query Parameters**:

| Param | Type | Default | Description |
|---|---|---|---|
| `period` | enum | `5m` | `5m`, `1h`, `24h` |

**Response**:

```json
{
  "peering_id": "...",
  "period": "5m",
  "bytes_in": 1048576000,
  "bytes_out": 524288000,
  "packets_in": 720000,
  "packets_out": 360000,
  "packets_dropped": 150,
  "drop_reasons": {
    "acl_deny": 100,
    "rate_limit": 50
  },
  "route_count": 12,
  "measured_at": "2026-04-06T12:00:00Z"
}
```

#### FR-8.2: Peering Event Log

**Trigger**: `GET /v1/peerings/{peering_id}/events`

**Authorization**: Caller must own either the requester or accepter VPC. Minimum role: `viewer`. Both sides of a cross-account peering see the same event log (events are per-peering, not per-account). Events do not leak account-internal details — they reference VPC IDs but not VPC names, CIDRs, or account identifiers of the peer.

Returns a time-ordered log of events for the peering:

| Event Type | Description |
|---|---|
| `peering_created` | Peering request submitted |
| `peering_accepted` | Cross-account peering accepted |
| `peering_provisioned` | Routes installed, peering is active |
| `peering_deleted` | Peering removed |
| `route_added` | New prefix advertised |
| `route_withdrawn` | Prefix withdrawn |
| `route_filtered` | Prefix blocked by route policy |
| `max_prefix_limit_reached` | Peer exceeded max-prefix limit |
| `state_changed` | Peering state transition (e.g., active → degraded) |
| `cross_region_connectivity_lost` | Inter-region DCI failure detected |
| `cross_region_connectivity_restored` | Inter-region DCI restored |
| `policy_updated` | Route policy changed |
| `bandwidth_limit_exceeded` | Traffic rate exceeded configured bandwidth cap |
| `peering_pending_deletion` | Cross-account peering deletion initiated (24h grace period) |
| `deletion_cancelled` | Pending deletion was cancelled |
| `peering_force_deleted` | Cross-account peering was force-deleted (skipped grace period) |
| `peering_disabled` | Peering disabled (routes withdrawn, config preserved) |
| `peering_enabled` | Disabled peering re-enabled (routes re-provisioned) |
| `direction_changed` | Peering direction was modified |
| `provisioning_churn_detected` | Repeated provisioning failures detected, backoff increased |
| `provisioning_permanently_failed` | Provisioning retries exhausted (10 attempts) |

Events are retained for 90 days.

#### FR-8.3: VPC Route Table View

**Trigger**: `GET /v1/vpcs/{vpc_id}/effective-routes`

**Authorization**: Caller must own the VPC. Minimum role: `viewer`. This endpoint only shows routes for peerings where the caller's account owns this VPC — it does not expose routes from peerings where this VPC is the peer side of a cross-account peering owned by another account.

Returns the consolidated effective route table for a VPC, showing all routes from all peerings. This is the customer's view of "what can this VPC reach?".

| Field | Description |
|---|---|
| `prefix` | Destination CIDR |
| `peering_id` | Which peering provides this route |
| `peer_vpc_id` | Which VPC originates this route |
| `origin` | `direct`, `transit`, `static` |
| `state` | `active`, `filtered` |
| `preference` | Route preference (lower = more preferred) |

---

### FR-9: Metering and Billing

#### FR-9.1: Metering Granularity

Data transfer is metered per peering, per direction, per 1-minute interval.

| Dimension | Granularity |
|---|---|
| Peering | Per peering ID |
| Direction | Ingress / egress (relative to each VPC) |
| Region type | Same-region vs. cross-region |
| Time | 1-minute buckets |

#### FR-9.2: Billing Events

The metering daemon emits billing events to the billing pipeline (Kafka/NATS):

```json
{
  "peering_id": "...",
  "account_id": "...",
  "vpc_id": "...",
  "direction": "egress",
  "region_type": "cross_region",
  "bytes": 10485760,
  "timestamp": "2026-04-06T12:01:00Z"
}
```

**Billing model** (configurable by billing team):
- Same-region data transfer: free or per-GB charge (configurable)
- Cross-region data transfer: per-GB charge, rate varies by region pair

#### FR-9.3: Customer Usage API

**Trigger**: `GET /v1/usage/peerings/{peering_id}`

**Authorization**: Caller must own either the requester or accepter VPC. Minimum role: `viewer`. For cross-account peerings, each side sees only their own account's billable usage (egress from their VPC).

**Query Parameters**:

| Param | Type | Description |
|---|---|---|
| `start_time` | ISO 8601 | Start of query window |
| `end_time` | ISO 8601 | End of query window |
| `granularity` | enum | `hourly`, `daily` |

**Response**: Time-series of bytes transferred per direction, broken down by same-region vs. cross-region.

---

### FR-10: Security and Access Control

#### FR-10.1: Authentication

All API requests must include one of:
- **API key**: Long-lived, account-scoped. Passed in `Authorization: Bearer <key>` header. API keys are opaque tokens mapped server-side to an `(account_id, key_id, role, scopes)` tuple.
- **JWT token**: Short-lived (1 hour), obtained from the platform's identity service. Passed in `Authorization: Bearer <jwt>` header. JWT claims must include `sub` (user/service ID), `account_id`, `role`, and `scopes`.

Unauthenticated requests receive `401 Unauthorized`.

**API key management**:
- API keys are created via the platform's IAM system (outside this product's scope) and registered with the VPC Interconnect service.
- Each key has an associated **role** (`admin`, `operator`, `viewer`) and optional **scope** restrictions.
- API keys can be rotated without downtime by supporting two active keys per account simultaneously.

#### FR-10.2: Authorization Model

Authorization is enforced at two layers: **resource ownership** (which account owns the resource) and **role-based access** (what the caller's role permits within their account).

**Roles**:

| Role | Permissions |
|---|---|
| `admin` | Full access: create, update, delete VPCs and peerings. Manage route policies. Accept/reject cross-account peerings. Force-delete. |
| `operator` | Create and manage peerings and routes. Cannot delete VPCs. Cannot force-delete cross-account peerings. |
| `viewer` | Read-only: get/list VPCs, peerings, routes, events, metrics. Cannot create, update, or delete any resource. |

**Resource-level authorization**:

| Operation | Ownership Rule | Minimum Role |
|---|---|---|
| Create VPC | Caller's account | `operator` |
| Delete VPC | Caller must own the VPC | `admin` |
| Read VPC details | Caller must own the VPC | `viewer` |
| List VPCs | Returns only caller's account VPCs | `viewer` |
| Create peering | Caller must own the requester VPC | `operator` |
| Accept/reject peering | Caller must own the accepter VPC | `operator` |
| Update peering (policy/direction) | Caller must own the requester VPC | `operator` |
| Delete peering (same-account) | Caller must own either VPC | `operator` |
| Delete peering (cross-account) | Caller must own either VPC | `operator` (`admin` for force-delete) |
| Cancel pending deletion | Caller must be the deletion initiator | `operator` |
| Disable/enable peering | Caller must own either VPC | `operator` |
| Read peering | Caller must own either VPC | `viewer` |
| List peerings | Returns peerings where caller's account owns at least one side | `viewer` |
| List peering routes | Caller must own either VPC | `viewer` |
| Override routes (add/withdraw) | Caller must own the originating VPC (see FR-4.2) | `operator` |
| Read effective routes (VPC) | Caller must own the VPC | `viewer` |
| Read peering events | Caller must own either VPC | `viewer` |
| Read peering metrics | Caller must own either VPC | `viewer` |
| Read usage/billing data | Caller must own either VPC | `viewer` |

Cross-account visibility is limited (see FR-3.3).

**Scope restrictions** (optional, on API keys):
- `vpc:<vpc-id>` — key can only operate on the specified VPC and its peerings
- `region:<region-id>` — key can only operate on resources in the specified region
- If scopes are set, operations outside the scope return `403 PERMISSION_DENIED`

#### FR-10.3: Audit Logging

All mutating operations are logged to an append-only audit log with:

| Field | Description |
|---|---|
| `timestamp` | ISO 8601 timestamp |
| `request_id` | Unique request identifier |
| `account_id` | Account that performed the action |
| `caller_id` | User/service ID from JWT `sub` claim or API key ID |
| `action` | Operation performed (e.g., `create_peering`, `delete_vpc`, `force_delete_peering`) |
| `resource_type` | `vpc`, `peering`, `route` |
| `resource_id` | UUID of the affected resource |
| `details` | Operation-specific details (e.g., old/new state, policy changes) |
| `source_ip` | Caller's IP address |

Audit logs are retained for 1 year (configurable). They are queryable by account but not directly exposed via the customer API in Phase 1 (exposed via internal admin tooling).

**Security-critical events** that are always logged regardless of audit log configuration:
- Cross-account peering acceptance/rejection
- Force deletion of cross-account peerings
- Peering direction changes
- Route policy changes that widen access (e.g., removing `denied_prefixes`)

#### FR-10.4: Rate Limiting

| Endpoint Category | Rate Limit |
|---|---|
| Mutating operations (create, update, delete) | 60 requests/minute per account |
| Read operations (get, list) | 300 requests/minute per account |
| Metrics endpoints | 120 requests/minute per account |
| Route overrides (`POST .../routes`) | 30 requests/minute per account |
| Policy updates (`PATCH /v1/peerings/{id}`) | 10 requests/minute per peering |

Exceeding the limit returns `429 Too Many Requests` with a `Retry-After` header.

**Per-peering policy update rate limit**: Route policy changes trigger BGP re-evaluation and fabric convergence. Rapid policy changes cause control-plane churn. The per-peering limit of 10/minute prevents a single peering from generating excessive BGP updates. This limit is enforced server-side and cannot be increased.

#### FR-10.5: CIDR Restrictions

- Only RFC 1918 private address ranges are allowed for VPC CIDRs: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- The system rejects VPCs with public IP ranges
- CIDR blocks must be between /16 and /28 (inclusive)
- A single VPC can have up to 5 non-overlapping CIDR blocks

---

## 5. Non-Functional Requirements

### NFR-1: Performance

| Metric | Target |
|---|---|
| Peering provisioning time (API call to active traffic) | < 30 seconds (same-region), < 60 seconds (cross-region) |
| Route convergence on failure | < 1 second (BFD + EVPN withdrawal) |
| Data plane throughput | Line rate (no software hop for standard peering) |
| Data plane latency overhead | < 10 microseconds added by VXLAN encap/decap (switch ASIC) |
| API response time (p99) | < 200ms for reads, < 500ms for writes |

### NFR-2: Availability

| Component | Target |
|---|---|
| Data plane (traffic forwarding) | 99.999% (5 nines). Fabric-native, no single software SPOF |
| Control plane (route management) | 99.99% (4 nines). Graceful restart preserves forwarding on control plane failure |
| Management plane (API) | 99.95%. Stateless N+1 replicas |

### NFR-3: Durability

- Peering configuration is persisted in PostgreSQL with synchronous replication
- VNI allocations use soft-delete with 7-day grace period to prevent reuse races
- Tombstones (deleted peerings/VPCs) retained for 30 days for audit

### NFR-4: Consistency

- Peering creation is **strongly consistent**: once the API returns `active`, traffic can flow
- Route updates are **eventually consistent** with a convergence window of < 5 seconds
- Metrics are **eventually consistent** with a delay of up to 60 seconds

### NFR-5: Scalability

See [Section 9: Quotas and Limits](#9-quotas-and-limits) for per-resource limits.

---

## 6. API Specification

### 6.1: Endpoints Summary

| Method | Path | Description | Phase |
|---|---|---|---|
| `POST` | `/v1/vpcs` | Register a VPC | 1 |
| `GET` | `/v1/vpcs/{vpc_id}` | Get VPC details | 1 |
| `GET` | `/v1/vpcs` | List VPCs | 1 |
| `DELETE` | `/v1/vpcs/{vpc_id}` | Delete VPC | 1 |
| `GET` | `/v1/vpcs/{vpc_id}/effective-routes` | Consolidated route table | 1 |
| `POST` | `/v1/peerings` | Create peering | 1 |
| `GET` | `/v1/peerings/{peering_id}` | Get peering | 1 |
| `GET` | `/v1/peerings` | List peerings | 1 |
| `PATCH` | `/v1/peerings/{peering_id}` | Update peering policy | 1 |
| `DELETE` | `/v1/peerings/{peering_id}` | Delete peering | 1 |
| `POST` | `/v1/peerings/{peering_id}/accept` | Accept cross-account peering | 1 |
| `POST` | `/v1/peerings/{peering_id}/reject` | Reject cross-account peering | 1 |
| `POST` | `/v1/peerings/{peering_id}/cancel-deletion` | Cancel pending cross-account deletion | 1 |
| `POST` | `/v1/peerings/{peering_id}/disable` | Disable peering (withdraw routes, preserve config) | 1 |
| `POST` | `/v1/peerings/{peering_id}/enable` | Re-enable disabled peering (re-provision routes) | 1 |
| `GET` | `/v1/peerings/{peering_id}/routes` | List effective routes | 1 |
| `POST` | `/v1/peerings/{peering_id}/routes` | Add/withdraw route override | 1 |
| `GET` | `/v1/peerings/{peering_id}/events` | Peering event log | 1 |
| `GET` | `/v1/metrics/peerings/{peering_id}` | Peering throughput metrics | 2 |
| `GET` | `/v1/usage/peerings/{peering_id}` | Billing usage data | 2 |
| `POST` | `/v1/transit-gateways` | Create transit gateway | 3 |
| `GET` | `/v1/transit-gateways/{tgw_id}` | Get transit gateway | 3 |
| `POST` | `/v1/transit-gateways/{tgw_id}/attachments` | Attach VPC | 3 |
| `GET` | `/v1/transit-gateways/{tgw_id}/route-table` | View route table | 3 |

### 6.2: Resource Schemas

#### VPC

```json
{
  "id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "account_id": "acct-001",
  "region_id": "us-east-1",
  "name": "production-vpc",
  "cidr_blocks": ["10.0.0.0/16"],
  "vni": 1048577,
  "vrf_name": "vpc-a1b2c3d4",
  "state": "active",
  "peering_count": 3,
  "created_at": "2026-04-06T10:00:00Z"
}
```

#### Peering

```json
{
  "id": "p-11223344-5566-7788-99aa-bbccddeeff00",
  "account_id": "acct-001",
  "requester_vpc_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "accepter_vpc_id": "f0e1d2c3-b4a5-6789-0fed-cba987654321",
  "direction": "bidirectional",
  "state": "active",
  "health": "healthy",
  "cross_region": false,
  "measured_latency_ms": null,
  "route_policy": {
    "allowed_prefixes": null,
    "denied_prefixes": [],
    "max_prefixes": 100,
    "bandwidth_limit_mbps": null
  },
  "route_count": 4,
  "deletion_scheduled_at": null,
  "deletion_initiated_by": null,
  "created_at": "2026-04-06T10:05:00Z",
  "provisioned_at": "2026-04-06T10:05:22Z"
}
```

**Peering states**: `pending_acceptance`, `provisioning`, `active`, `degraded`, `disabled`, `pending_deletion`, `deleting`, `failed`, `expired`, `rejected`, `deleted`

#### Route Entry

```json
{
  "prefix": "10.2.0.0/16",
  "origin_vpc_id": "f0e1d2c3-b4a5-6789-0fed-cba987654321",
  "state": "active",
  "origin": "direct",
  "next_hop": "192.168.100.42",
  "preference": 100
}
```

#### Event

```json
{
  "id": "evt-001",
  "peering_id": "p-11223344-5566-7788-99aa-bbccddeeff00",
  "type": "peering_provisioned",
  "message": "Peering is now active. 4 routes installed.",
  "timestamp": "2026-04-06T10:05:22Z"
}
```

### 6.3: Common Response Envelope

All responses use a consistent envelope:

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
  "pagination": {
    "next_page_token": "eyJ...",
    "total_count": 42
  },
  "request_id": "req-abc123"
}
```

**Error**:
```json
{
  "error": {
    "code": "CIDR_OVERLAP",
    "message": "VPC CIDR 10.0.0.0/16 overlaps with existing VPC vpc-xyz (10.0.0.0/8)",
    "details": {
      "conflicting_vpc_id": "xyz",
      "conflicting_cidr": "10.0.0.0/8"
    }
  },
  "request_id": "req-abc123"
}
```

All responses include `request_id` for tracing.

---

## 7. User Flows

### 7.1: Same-Account, Same-Region Peering (Happy Path)

```
Customer                  API                Controller          BGP Service         Agent
   │                       │                     │                    │                │
   ├── POST /v1/peerings ──►                     │                    │                │
   │                       ├── validate ──────────►                    │                │
   │                       │   (same account →    │                    │                │
   │                       │    auto-accept)      │                    │                │
   │                       │                     ├── program RT ──────►                │
   │                       │                     │   import/export     │                │
   │                       │                     │                    ├── inject        │
   │                       │                     │                    │   Type-5 routes │
   │                       │                     │                    │                │
   │                       │                     ├── program hosts ───┼───────────────►│
   │                       │                     │                    │                ├── VRF +
   │                       │                     │                    │                │   ACL rules
   │                       │                     │◄── routes confirmed┼────────────────┤
   │                       │                     │                    │                │
   │   ◄── 201 Created ────┤                     │                    │                │
   │   (state: active)     │                     │                    │                │
   │                       │                     │                    │                │
   │                   Traffic flows between VPCs at line rate        │                │
```

**Elapsed time**: < 30 seconds from API call to active traffic.

### 7.2: Cross-Account Peering

```
Requester (Acct A)         API              Accepter (Acct B)
   │                        │                      │
   ├── POST /v1/peerings ──►│                      │
   │   (accepter_vpc owned  │                      │
   │    by Acct B)          │                      │
   │                        │                      │
   │   ◄── 201 Created ────┤                      │
   │   (state: pending_     │                      │
   │    acceptance)         │                      │
   │                        │                      │
   │                        │  (Acct B discovers   │
   │                        │   pending peering    │
   │                        │   via list/webhook)  │
   │                        │                      │
   │                        │◄── POST /accept ─────┤
   │                        │                      │
   │                        ├── provision ─────────►│
   │                        │                      │
   │                        │   (both accounts see │
   │                        │    state: active)    │
```

### 7.3: Peering Failure and Recovery

```
Time    Event                           Peering State    Customer Action Required
─────   ─────                           ─────────────    ────────────────────────
T+0     Link failure between hosts      active           None
T+300ms BFD detects failure             active→degraded  None
T+400ms BGP withdraws affected routes   degraded         None
T+400ms Traffic fails over to backup    degraded         None
        path (if available)
T+???   Link repaired                   degraded         None
T+???   BFD session re-established      degraded→active  None
T+???   Routes re-advertised            active           None
```

---

## 8. Error Handling

### 8.1: Error Codes

| HTTP Status | Error Code | Description |
|---|---|---|
| 400 | `INVALID_INPUT` | Malformed request (bad JSON, missing fields, invalid CIDR format) |
| 400 | `INVALID_CIDR` | CIDR is not a valid RFC 1918 private range or is outside /16–/28 |
| 401 | `UNAUTHENTICATED` | Missing or invalid authentication credentials |
| 403 | `PERMISSION_DENIED` | Caller does not have access to the target resource |
| 404 | `NOT_FOUND` | Resource does not exist |
| 409 | `CIDR_OVERLAP` | VPC CIDR overlaps with an existing VPC in the same account |
| 409 | `DUPLICATE_PEERING` | Peering already exists between these two VPCs |
| 409 | `HAS_ACTIVE_PEERINGS` | Cannot delete VPC with active peerings |
| 409 | `INVALID_STATE` | Operation not allowed in the current peering state |
| 422 | `QUOTA_EXCEEDED` | Resource limit reached (VPC count, peering count, prefix count) |
| 422 | `VNI_EXHAUSTED` | No VNIs available in the target region |
| 429 | `RATE_LIMITED` | Too many requests. Retry after the time specified in `Retry-After` header |
| 429 | `POLICY_UPDATE_RATE_LIMITED` | Policy updates too frequent for this peering (max 10/minute) |
| 500 | `INTERNAL` | Unexpected server error. Include `request_id` in support tickets |
| 503 | `UNAVAILABLE` | Service temporarily unavailable (e.g., during deployment) |

### 8.2: Idempotency

Create operations (`POST /v1/peerings`, `POST /v1/vpcs`) accept an optional `Idempotency-Key` header (UUID). If the same key is resubmitted within 24 hours:
- If the original request succeeded, return the original response with `200 OK`
- If the original request failed, re-execute the request

This prevents duplicate resource creation from retries.

### 8.3: Provisioning Failure

If peering provisioning fails (e.g., BGP service unreachable, agent cannot program routes):

1. State transitions to `failed`
2. An event is emitted with diagnostic details (failure reason, which step failed, retry count)
3. The peering can be deleted and recreated
4. The controller retries failed peerings with **exponential backoff**: 30s, 60s, 120s, 240s, 480s, then capped at 480s for remaining retries (10 retries total)
5. After 10 retries, state remains `failed` and no further automatic retries occur
6. A `provisioning_permanently_failed` event is emitted after the 10th retry

**Retry safeguards**:
- Each retry attempt is **idempotent**: the controller checks the current state of BGP routes and agent VRFs before re-executing steps. If step 1 (RT import/export) succeeded on a previous attempt, it is not re-executed. Only failed/incomplete steps are retried.
- **Provisioning deduplication**: The controller tracks in-progress provisioning operations by peering ID. If a provisioning operation is already in progress for a peering, a new attempt is not started. This prevents duplicate provisioning from concurrent controller operations (e.g., during leader failover).
- **Rollback on failure**: If provisioning fails partway through (e.g., RT import succeeded but route injection failed), the controller attempts to roll back completed steps before marking as `failed`. If rollback also fails, the partially-provisioned state is logged and the reconciler (see Architecture spec Section 4.4) will detect and clean up orphaned state.
- **Churn detection**: If the same peering fails provisioning 3 times within 5 minutes, the controller emits a `provisioning_churn_detected` event and increases the backoff floor to 5 minutes. This prevents a systematically failing peering from consuming controller capacity.

### 8.4: Control-Plane Stability

**Route update coalescing**: When multiple peering operations occur simultaneously (e.g., bulk peering creation via Terraform), the controller batches BGP operations into windows of 2 seconds. All route advertisements and withdrawals within a window are submitted to the BGP service as a single batch, reducing the number of BGP UPDATE messages sent to route reflectors.

**BGP update budget**: The BGP service enforces a maximum of **1,000 route updates per second** to the fabric. If the update queue exceeds this rate, updates are queued and processed in order. This prevents a burst of peering operations from overwhelming route reflectors or causing fabric-wide convergence events.

**Circuit breaker**: If the BGP service detects that >50% of its route operations are failing (e.g., RR is unreachable), it enters a **circuit-breaker open** state: new route operations are rejected with a transient error for 30 seconds, then a single probe operation is attempted. If the probe succeeds, normal operation resumes. This prevents cascading failures where a down RR causes unbounded retry storms.

---

## 9. Quotas and Limits

### 9.1: Default Quotas

| Resource | Default Limit | Maximum (requestable) |
|---|---|---|
| VPCs per account per region | 100 | 500 |
| CIDR blocks per VPC | 5 | 5 (hard limit) |
| CIDR range | /16 to /28 | /16 to /28 (hard limit) |
| Peerings per VPC | 125 | 500 |
| Total peerings per account | 1,000 | 10,000 |
| Prefixes per peering (max_prefixes) | 100 | 1,000 |
| Static route overrides per peering | 50 | 200 |
| Transit gateways per account per region | 5 | 20 |
| VPC attachments per transit gateway | 50 | 500 |
| Event log retention | 90 days | 90 days (hard limit) |
| Pending peering expiry | 7 days | 7 days (hard limit) |

### 9.2: Quota Increase Requests

Customers can request quota increases through the platform's support/quota system. Increases above the "Maximum" column require architectural review.

---

## 10. Phased Delivery

### Phase 1: Single-Region Peering

**Customer capabilities delivered**:
- Register VPCs with the interconnect system
- Create/delete peerings between VPCs in the same region and same account
- Create/accept/reject cross-account peerings
- Configure route policies (allowed/denied prefixes, max-prefix limits)
- View effective routes and peering event log
- Peering health status and state machine

**Not included**: cross-region peering, throughput metrics, billing metering, transit gateways, bandwidth limits

### Phase 2: Multi-Region + Metering

**Additional capabilities**:
- Cross-region peering (same API, automatic detection)
- Per-peering throughput metrics (`GET /v1/metrics/peerings/{id}`)
- Billing usage API (`GET /v1/usage/peerings/{id}`)
- Bandwidth limit enforcement (XDP-based)
- Latency measurement for cross-region peerings

### Phase 3: Transit Gateway

**Additional capabilities**:
- Transit gateway CRUD and VPC attachment
- Hub-and-spoke routing with spoke isolation
- Transit gateway route tables
- Multi-region transit gateway
