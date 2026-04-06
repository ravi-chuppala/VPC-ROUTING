-- VPC Interconnect initial schema

CREATE TABLE IF NOT EXISTS vpcs (
    id UUID PRIMARY KEY,
    account_id UUID NOT NULL,
    region_id TEXT NOT NULL,
    name TEXT NOT NULL,
    cidr_blocks TEXT[] NOT NULL,
    vni INTEGER NOT NULL UNIQUE,
    vrf_name TEXT NOT NULL,
    rd TEXT NOT NULL,
    export_rt TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE(account_id, region_id, name)
);

CREATE INDEX idx_vpcs_account_region ON vpcs(account_id, region_id);

CREATE TABLE IF NOT EXISTS peerings (
    id UUID PRIMARY KEY,
    account_id UUID NOT NULL,
    requester_vpc_id UUID NOT NULL REFERENCES vpcs(id),
    accepter_vpc_id UUID NOT NULL REFERENCES vpcs(id),
    direction TEXT NOT NULL DEFAULT 'bidirectional',
    state TEXT NOT NULL DEFAULT 'pending_acceptance',
    cross_region BOOLEAN NOT NULL DEFAULT FALSE,
    latency_ms DOUBLE PRECISION,
    route_policy JSONB NOT NULL DEFAULT '{}',
    route_count INTEGER NOT NULL DEFAULT 0,
    region_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provisioned_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_peerings_account ON peerings(account_id);
CREATE INDEX idx_peerings_requester ON peerings(requester_vpc_id);
CREATE INDEX idx_peerings_accepter ON peerings(accepter_vpc_id);
CREATE INDEX idx_peerings_state ON peerings(state);

CREATE TABLE IF NOT EXISTS peering_events (
    id UUID PRIMARY KEY,
    peering_id UUID NOT NULL REFERENCES peerings(id),
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_events_peering ON peering_events(peering_id, created_at DESC);

CREATE TABLE IF NOT EXISTS route_overrides (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    peering_id UUID NOT NULL REFERENCES peerings(id),
    prefix TEXT NOT NULL,
    origin_vpc_id UUID,
    origin TEXT NOT NULL DEFAULT 'static',
    state TEXT NOT NULL DEFAULT 'active',
    preference INTEGER NOT NULL DEFAULT 200,
    next_hop TEXT,
    vni INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(peering_id, prefix)
);

CREATE INDEX idx_routes_peering ON route_overrides(peering_id);

CREATE TABLE IF NOT EXISTS vni_allocations (
    vni INTEGER PRIMARY KEY,
    vpc_id UUID REFERENCES vpcs(id),
    account_id UUID NOT NULL,
    region_id TEXT NOT NULL,
    allocated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ,
    grace_expires_at TIMESTAMPTZ
);
