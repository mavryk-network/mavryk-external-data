# ADR-0005: Keep GORM (defer `sqlc` migration)

- **Status**: Accepted (revisit if SQL surface grows)
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

The greenfield long-format rewrite shifted the SQL surface area:

- Hot-path queries are non-trivial: `DISTINCT ON (currency)`,
  `time_bucket`, continuous aggregates.
- We have ~5 distinct queries per repository, plus simple Save/Get.
- GORM was already wired in pre-rewrite. Removing it means rewriting
  every storage entity and repository.

`sqlc` would generate typed Go functions from `.sql` files — eliminates
reflection and produces single-statement type-safe calls. Attractive for
a long-format model where most queries are hand-written anyway.

## Decision

**Keep GORM**. Continue using its `Clauses(OnConflict{...})` for upsert,
`Raw(...).Scan(...)` for `DISTINCT ON` and aggregate queries, and
struct-tagged entities for the table mapping.

Where queries get unwieldy in GORM's builder, drop to `Raw` — that's
already the pattern in `token_price_repository.go`.

## Consequences

- ✅ Zero migration cost now. The team is fluent in GORM idioms.
- ✅ Mixed builder/Raw approach reads cleanly: simple operations use the
  builder, complex queries use Raw. Tests verify the SQL.
- ⚠️ Reflection-driven mapping is slower than generated code. Not on a
  measured-hot path today; revisit if it becomes one.
- ⚠️ GORM's hook system (BeforeCreate, etc.) is unused — we're paying
  the surface cost without using the value.
- 🔁 Revisit triggers (any one):
  - Storage layer crosses ~20 distinct queries and the boilerplate
    becomes a tax.
  - We hit a real GORM bug or limitation that's expensive to work
    around (e.g. no batch upsert with returning).
  - We onboard team members who object to GORM-style ORMs as a
    consistent pattern.

## Alternatives considered

- **`sqlc`** — generates typed Go from SQL. The right call for a
  query-heavy service. We chose to defer because the migration is a
  full-day rewrite of every repository and ten entities, and GORM
  isn't actively painful. If/when we revisit migrations
  ([ADR-0004](0004-migration-tool-psql-loop.md)), bundling a sqlc
  migration is the natural moment.
- **`bun` (uptrace)** — more composable than GORM, less verbose than
  raw SQL, but we'd be swapping ORMs for marginal gain.
- **`database/sql` + `pgx` directly, no ORM** — maximum control, most
  boilerplate. Suits a 100k-LOC trading system, not us at 6k LOC.

## Notes

- Repositories: `internal/core/infrastructure/storage/repositories/`.
- Entities: `internal/core/infrastructure/storage/entities/`.
- This decision is the open follow-up to
  `upgrade-plan.md` §6.4 (historical; not in this repo).
