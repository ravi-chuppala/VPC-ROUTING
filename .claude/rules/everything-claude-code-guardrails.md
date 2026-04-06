# VPC Routing Guardrails

## Error Handling
- All errors must be wrapped: `fmt.Errorf("package/func: %w", err)`
- Never use `_` to discard errors from store or network calls
- Domain errors use `model.ErrXxx()` constructors with HTTP status codes

## Concurrency
- Pass `context.Context` as first parameter on all goroutine-spawning functions
- Use `defer cancel()` immediately after `context.WithTimeout`
- Use `sync.Once` for idempotent Stop/Close methods

## Database
- Migrations in `migrations/` only — no schema changes in application code
- Use pgx parameterized queries, never string concatenation
- Check `RowsAffected()` on UPDATE/DELETE operations

## Testing
- Table-driven tests with descriptive names
- Use in-memory store implementations for unit tests
- Run with `-race` flag

## Commits
- Conventional format: `feat:`, `fix:`, `test:`, `docs:`
- One logical change per commit

## Architecture
- API handler sets state only; controller reconciler handles provisioning
- All store operations go through interfaces (VPCStore, PeeringStore, etc.)
- No circular package dependencies
