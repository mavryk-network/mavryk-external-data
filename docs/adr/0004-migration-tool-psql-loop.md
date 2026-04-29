# ADR-0004: Plain-SQL migrations via Makefile psql-loop

- **Status**: Accepted (revisit after 12 months)
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

For schema migrations we had three credible options:

1. **Plain `*.sql` files run in lex order** by `psql -f` (current setup).
   The Makefile drives `make migrate-up`; docker-compose's
   `/docker-entrypoint-initdb.d` does the same on first DB start.
2. **`pressly/goose`** — Go-native, embedded migrations, up/down pairs,
   status command, programmatic API.
3. **`golang-migrate/migrate`** — most popular, similar feature surface,
   slightly more brittle source-driver story.
4. **`atlas`** — declarative schema, diff-driven, schema-as-code.

The greenfield rewrite [upgrade-plan.md](../../upgrade-plan.md) listed
goose as the recommended target, but the implementation kept the
psql-loop because the cost of the migration was non-trivial and the
benefits weren't blocking.

## Decision

**Keep the plain-SQL psql-loop for now.** Reassess after 12 months or
when one of the trigger conditions below fires.

## Consequences

- ✅ Zero new dependencies; CI and local dev stay simple.
- ✅ Migrations are inspectable as plain SQL; ops can run them by hand
  during incidents without learning a tool's semantics.
- ✅ Identical ordering between the Makefile loop and
  `/docker-entrypoint-initdb.d` — one mental model.
- ⚠️ No down-migrations. We rely on forward-only schemas and `IF NOT EXISTS`
  guards. Acceptable for a service that is currently single-writer; not
  acceptable forever.
- ⚠️ No `migrate status`-equivalent. To know what's applied, we must read
  the DB. Today the answer is "all of them" because the loop is idempotent.
- ⚠️ Can't run a single migration in isolation — the loop runs everything
  every time. Idempotency keeps this safe but slow on large schemas.
- 🔁 Revisit triggers (any one):
  - We need rollback semantics (e.g. for multi-region rollouts).
  - More than one developer is racing migrations against shared databases.
  - We add a `migrations` tracking table for explicit applied-state.

## Alternatives considered

- **goose** — preferred replacement when we revisit. Go-embedding plays
  well with our binary, supports up/down, status is one command. Cost is
  ~half a day of integration plus retraining ops on the new commands.
- **golang-migrate/migrate** — comparable to goose. Marginally less
  ergonomic when embedding migrations into a Go binary. Wouldn't pick
  unless we need a non-PG driver.
- **atlas** — overkill for this project's size. Powerful for teams with
  strong DB discipline and many environments; we have neither yet.

## Notes

- Migrations live in `migrations/0001_*.sql` … `0009_seed.sql`.
- Run via `make migrate-up`. The dockerfile's migration stage uses
  `scripts/run-migrations.sh`.
- This decision is the open follow-up to
  [upgrade-plan.md §6.3](../../upgrade-plan.md).
