# VPC Interconnect Service

Go project providing fabric-native VPC peering for bare-metal cloud infrastructure. Four services: API, controller, BGP, agent.

## Commands
- Build: `make build` (produces bin/api, bin/controller, bin/bgp, bin/agent)
- Test: `make test` (runs with -race, 57+ tests across 8 packages)
- Lint: `make lint` (go vet)
- Dev DB: `docker-compose up -d` (PostgreSQL 16)

## Key Packages
- `internal/api` — REST HTTP handlers and router
- `internal/controller` — peering provisioner and reconciliation loop
- `internal/bgp` — BGP/EVPN Type-5 route builder and service interface
- `internal/agent` — netlink VRF/VXLAN/ACL manager and drift reconciler
- `internal/store` — PostgreSQL and in-memory store implementations
- `internal/model` — domain types, CIDR validation, route policy, errors
- `internal/auth` — JWT/API-key middleware, ownership checks, rate limiter
- `internal/vni` — VNI allocator (24-bit partitioned space)

## Architecture
- API sets peering state; controller reconciler handles provisioning (single path)
- BGP service injects EVPN Type-5 routes via RT import/export
- Agent programs Linux VRFs, VXLAN interfaces, nftables ACLs via netlink
- All stores implement interfaces for testability (in-memory for tests, PostgreSQL for production)

## Conventions
- Conventional commits: `feat:`, `fix:`, `test:`, `docs:`
- All errors wrapped: `fmt.Errorf("context: %w", err)`
- Table-driven tests in `*_test.go` co-located with package
- `context.Context` first parameter on all service methods
- Migrations in `migrations/` (up/down SQL files)

## Git
- Feature branches: `feature/description`
- One logical change per commit
