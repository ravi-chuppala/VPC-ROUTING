-- Performance indexes identified by performance review

-- Controller reconciler queries peerings by state on every tick
CREATE INDEX IF NOT EXISTS idx_peerings_state_created ON peerings(state, created_at);

-- FindByVPCs needs to look up peerings between two VPCs
CREATE INDEX IF NOT EXISTS idx_peerings_vpc_pair ON peerings(requester_vpc_id, accepter_vpc_id);

-- CountByVPC queries by either VPC ID
CREATE INDEX IF NOT EXISTS idx_peerings_accepter_state ON peerings(accepter_vpc_id, state);
CREATE INDEX IF NOT EXISTS idx_peerings_requester_state ON peerings(requester_vpc_id, state);

-- FindByName partial unique index (excludes deleted)
CREATE UNIQUE INDEX IF NOT EXISTS idx_vpcs_name_unique ON vpcs(account_id, region_id, name) WHERE state != 'deleted';

-- VPC list ordered by created_at
CREATE INDEX IF NOT EXISTS idx_vpcs_account_created ON vpcs(account_id, region_id, created_at DESC);

-- Event list by peering ordered by time
CREATE INDEX IF NOT EXISTS idx_events_peering_time ON peering_events(peering_id, created_at DESC);
