package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ravi-chuppala/vpc-routing/internal/model"
)

// PostgresVPCStore implements VPCStore backed by PostgreSQL.
type PostgresVPCStore struct {
	pool *pgxpool.Pool
}

func NewPostgresVPCStore(pool *pgxpool.Pool) *PostgresVPCStore {
	return &PostgresVPCStore{pool: pool}
}

func (s *PostgresVPCStore) Create(ctx context.Context, vpc *model.VPC) error {
	cidrs := make([]string, len(vpc.CIDRBlocks))
	for i, c := range vpc.CIDRBlocks {
		cidrs[i] = c.String()
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO vpcs (id, account_id, region_id, name, cidr_blocks, vni, vrf_name, rd, export_rt, state, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		vpc.ID, vpc.AccountID, vpc.RegionID, vpc.Name, cidrs, vpc.VNI,
		vpc.VRFName, vpc.RD, vpc.ExportRT, string(vpc.State), vpc.CreatedAt,
	)
	return err
}

func (s *PostgresVPCStore) Get(ctx context.Context, id uuid.UUID) (*model.VPC, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, account_id, region_id, name, cidr_blocks, vni, vrf_name, rd, export_rt, state, created_at, deleted_at
		 FROM vpcs WHERE id = $1 AND state != 'deleted'`, id)

	vpc := &model.VPC{}
	var cidrs []string
	var state string
	err := row.Scan(&vpc.ID, &vpc.AccountID, &vpc.RegionID, &vpc.Name, &cidrs,
		&vpc.VNI, &vpc.VRFName, &vpc.RD, &vpc.ExportRT, &state, &vpc.CreatedAt, &vpc.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, model.ErrNotFound("VPC")
	}
	if err != nil {
		return nil, fmt.Errorf("scan VPC: %w", err)
	}
	vpc.State = model.VPCState(state)
	for _, c := range cidrs {
		p, _ := netip.ParsePrefix(c)
		vpc.CIDRBlocks = append(vpc.CIDRBlocks, p)
	}
	return vpc, nil
}

func (s *PostgresVPCStore) List(ctx context.Context, accountID uuid.UUID, regionID string, params ListParams) (*ListResult[model.VPC], error) {
	query := `SELECT id, account_id, region_id, name, cidr_blocks, vni, vrf_name, rd, export_rt, state, created_at
		 FROM vpcs WHERE account_id = $1 AND state != 'deleted'`
	args := []any{accountID}

	if regionID != "" {
		query += " AND region_id = $2"
		args = append(args, regionID)
	}
	query += " ORDER BY created_at DESC"

	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	query += fmt.Sprintf(" LIMIT %d", pageSize)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.VPC
	for rows.Next() {
		vpc := model.VPC{}
		var cidrs []string
		var state string
		if err := rows.Scan(&vpc.ID, &vpc.AccountID, &vpc.RegionID, &vpc.Name, &cidrs,
			&vpc.VNI, &vpc.VRFName, &vpc.RD, &vpc.ExportRT, &state, &vpc.CreatedAt); err != nil {
			return nil, err
		}
		vpc.State = model.VPCState(state)
		for _, c := range cidrs {
			p, _ := netip.ParsePrefix(c)
			vpc.CIDRBlocks = append(vpc.CIDRBlocks, p)
		}
		items = append(items, vpc)
	}
	return &ListResult[model.VPC]{Items: items, TotalCount: len(items)}, nil
}

func (s *PostgresVPCStore) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE vpcs SET state = 'deleted', deleted_at = $2 WHERE id = $1`, id, time.Now())
	return err
}

func (s *PostgresVPCStore) FindOverlappingCIDR(ctx context.Context, accountID uuid.UUID, cidrs []netip.Prefix) (*model.VPC, error) {
	// Check each CIDR against existing VPCs using application-level overlap detection.
	// A production implementation could use PostgreSQL inet operators.
	result, err := s.List(ctx, accountID, "", ListParams{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	for _, vpc := range result.Items {
		if model.CIDRsOverlap(vpc.CIDRBlocks, cidrs) {
			return &vpc, nil
		}
	}
	return nil, nil
}

func (s *PostgresVPCStore) FindByName(ctx context.Context, accountID uuid.UUID, regionID, name string) (*model.VPC, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id FROM vpcs WHERE account_id = $1 AND region_id = $2 AND name = $3 AND state != 'deleted'`,
		accountID, regionID, name)
	var id uuid.UUID
	err := row.Scan(&id)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *PostgresVPCStore) CountPeerings(ctx context.Context, vpcID uuid.UUID) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM peerings WHERE (requester_vpc_id = $1 OR accepter_vpc_id = $1) AND state NOT IN ('deleted', 'rejected', 'expired')`,
		vpcID).Scan(&count)
	return count, err
}

// PostgresPeeringStore implements PeeringStore backed by PostgreSQL.
type PostgresPeeringStore struct {
	pool *pgxpool.Pool
}

func NewPostgresPeeringStore(pool *pgxpool.Pool) *PostgresPeeringStore {
	return &PostgresPeeringStore{pool: pool}
}

func (s *PostgresPeeringStore) Create(ctx context.Context, p *model.Peering) error {
	policyJSON, _ := json.Marshal(p.RoutePolicy)
	_, err := s.pool.Exec(ctx,
		`INSERT INTO peerings (id, account_id, requester_vpc_id, accepter_vpc_id, direction, state, cross_region, latency_ms, route_policy, route_count, region_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		p.ID, p.AccountID, p.RequesterVPCID, p.AccepterVPCID,
		string(p.Direction), string(p.State), p.CrossRegion, p.LatencyMs,
		policyJSON, p.RouteCount, "default", p.CreatedAt,
	)
	return err
}

func (s *PostgresPeeringStore) Get(ctx context.Context, id uuid.UUID) (*model.Peering, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, account_id, requester_vpc_id, accepter_vpc_id, direction, state, cross_region, latency_ms, route_policy, route_count, created_at, provisioned_at, deleted_at
		 FROM peerings WHERE id = $1`, id)

	p := &model.Peering{}
	var direction, state string
	var policyJSON []byte
	err := row.Scan(&p.ID, &p.AccountID, &p.RequesterVPCID, &p.AccepterVPCID,
		&direction, &state, &p.CrossRegion, &p.LatencyMs, &policyJSON,
		&p.RouteCount, &p.CreatedAt, &p.ProvisionedAt, &p.DeletedAt)
	if err == pgx.ErrNoRows {
		return nil, model.ErrNotFound("peering")
	}
	if err != nil {
		return nil, fmt.Errorf("scan peering: %w", err)
	}
	p.Direction = model.PeeringDirection(direction)
	p.State = model.PeeringState(state)
	json.Unmarshal(policyJSON, &p.RoutePolicy)
	return p, nil
}

func (s *PostgresPeeringStore) List(ctx context.Context, accountID uuid.UUID, vpcID *uuid.UUID, state *model.PeeringState, params ListParams) (*ListResult[model.Peering], error) {
	query := `SELECT id, account_id, requester_vpc_id, accepter_vpc_id, direction, state, cross_region, route_count, created_at, provisioned_at
		 FROM peerings WHERE account_id = $1 AND state != 'deleted'`
	args := []any{accountID}
	argN := 2

	if vpcID != nil {
		query += fmt.Sprintf(" AND (requester_vpc_id = $%d OR accepter_vpc_id = $%d)", argN, argN)
		args = append(args, *vpcID)
		argN++
	}
	if state != nil {
		query += fmt.Sprintf(" AND state = $%d", argN)
		args = append(args, string(*state))
		argN++
	}
	query += " ORDER BY created_at DESC"

	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	query += fmt.Sprintf(" LIMIT %d", pageSize)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.Peering
	for rows.Next() {
		p := model.Peering{}
		var direction, st string
		if err := rows.Scan(&p.ID, &p.AccountID, &p.RequesterVPCID, &p.AccepterVPCID,
			&direction, &st, &p.CrossRegion, &p.RouteCount, &p.CreatedAt, &p.ProvisionedAt); err != nil {
			return nil, err
		}
		p.Direction = model.PeeringDirection(direction)
		p.State = model.PeeringState(st)
		items = append(items, p)
	}
	return &ListResult[model.Peering]{Items: items, TotalCount: len(items)}, nil
}

func (s *PostgresPeeringStore) Update(ctx context.Context, p *model.Peering) error {
	policyJSON, _ := json.Marshal(p.RoutePolicy)
	_, err := s.pool.Exec(ctx,
		`UPDATE peerings SET direction = $2, state = $3, route_policy = $4, route_count = $5, provisioned_at = $6, deleted_at = $7 WHERE id = $1`,
		p.ID, string(p.Direction), string(p.State), policyJSON, p.RouteCount, p.ProvisionedAt, p.DeletedAt)
	return err
}

func (s *PostgresPeeringStore) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE peerings SET state = 'deleted', deleted_at = $2 WHERE id = $1`, id, time.Now())
	return err
}

func (s *PostgresPeeringStore) FindByVPCs(ctx context.Context, vpcA, vpcB uuid.UUID) (*model.Peering, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id FROM peerings
		 WHERE ((requester_vpc_id = $1 AND accepter_vpc_id = $2) OR (requester_vpc_id = $2 AND accepter_vpc_id = $1))
		 AND state NOT IN ('deleted', 'rejected', 'expired')`, vpcA, vpcB)
	var id uuid.UUID
	err := row.Scan(&id)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *PostgresPeeringStore) CountByVPC(ctx context.Context, vpcID uuid.UUID) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM peerings WHERE (requester_vpc_id = $1 OR accepter_vpc_id = $1) AND state NOT IN ('deleted', 'rejected', 'expired')`,
		vpcID).Scan(&count)
	return count, err
}

func (s *PostgresPeeringStore) ListByState(ctx context.Context, state model.PeeringState) ([]model.Peering, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, account_id, requester_vpc_id, accepter_vpc_id, direction, state, cross_region, route_count, created_at, provisioned_at
		 FROM peerings WHERE state = $1`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.Peering
	for rows.Next() {
		p := model.Peering{}
		var direction, st string
		if err := rows.Scan(&p.ID, &p.AccountID, &p.RequesterVPCID, &p.AccepterVPCID,
			&direction, &st, &p.CrossRegion, &p.RouteCount, &p.CreatedAt, &p.ProvisionedAt); err != nil {
			return nil, err
		}
		p.Direction = model.PeeringDirection(direction)
		p.State = model.PeeringState(st)
		items = append(items, p)
	}
	return items, nil
}

// PostgresEventStore implements EventStore backed by PostgreSQL.
type PostgresEventStore struct {
	pool *pgxpool.Pool
}

func NewPostgresEventStore(pool *pgxpool.Pool) *PostgresEventStore {
	return &PostgresEventStore{pool: pool}
}

func (s *PostgresEventStore) Append(ctx context.Context, event *model.PeeringEvent) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO peering_events (id, peering_id, event_type, message, created_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		event.ID, event.PeeringID, string(event.Type), event.Message, event.Timestamp)
	return err
}

func (s *PostgresEventStore) List(ctx context.Context, peeringID uuid.UUID, params ListParams) (*ListResult[model.PeeringEvent], error) {
	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, peering_id, event_type, message, created_at
		 FROM peering_events WHERE peering_id = $1 ORDER BY created_at DESC LIMIT $2`,
		peeringID, pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.PeeringEvent
	for rows.Next() {
		e := model.PeeringEvent{}
		var eventType string
		if err := rows.Scan(&e.ID, &e.PeeringID, &eventType, &e.Message, &e.Timestamp); err != nil {
			return nil, err
		}
		e.Type = model.EventType(eventType)
		items = append(items, e)
	}
	return &ListResult[model.PeeringEvent]{Items: items, TotalCount: len(items)}, nil
}
