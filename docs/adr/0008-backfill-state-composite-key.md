# ADR-0008: Composite `(source, entity_key)` backfill state

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

Pre-rewrite, `backfill_state` had `token TEXT PRIMARY KEY`. The reverse-
backfill cursor and error/backoff bookkeeping were tied to a single
domain (FT prices, CoinGecko).

When RWA backfill enters scope (orderbook history, oracle replay), we
need state for `(source, entity)` pairs where `entity` may be a
token symbol, a `pair_id`, or a feed identifier. Two paths:

1. Two tables: `backfill_state_ft`, `backfill_state_rwa`. Two engines.
2. One table with composite PK and stringified `entity_key`.

## Decision

```sql
CREATE TABLE backfill_state (
    source_code      TEXT NOT NULL REFERENCES sources(code),
    entity_key       TEXT NOT NULL,        -- token symbol, pair_id::text, etc.
    oldest_ts        TIMESTAMPTZ,
    disabled         BOOLEAN, disabled_reason TEXT,
    error_count      INT, last_error TEXT,
    next_attempt_at  TIMESTAMPTZ,
    created_at, updated_at TIMESTAMPTZ,
    PRIMARY KEY (source_code, entity_key)
);

CREATE INDEX backfill_state_due
    ON backfill_state (source_code, disabled, next_attempt_at);
```

One table, one engine, one set of operational queries.

## Consequences

- ✅ Adding a new source's backfill is a code-only change — new job that
  uses the same `BackfillStateRepository`.
- ✅ Cross-source ops queries are uniform: "show me everything that's
  auto-disabled" is one `SELECT`, not two.
- ✅ The composite index `(source_code, disabled, next_attempt_at)`
  serves the hot-path "pick ready" query for every source.
- ⚠️ `entity_key` as TEXT loses type strength: a bug could write
  "mvrk" under `source=equiteez`. We mitigate by normalizing in Go
  (FT: lowercase token symbol; RWA: `strconv.FormatInt(pair_id, 10)`).
- ⚠️ Reset-on-manual-enable lives in `BackfillStateRepository.Upsert`
  rather than as a SQL trigger. Centralizes the rule but couples it
  to Go code; acceptable for a single-writer service.
- 🔁 If sources ever need radically different state shapes (e.g. a
  pause-and-resume cursor for streaming sources), we'd add a
  `state_extra JSONB` column rather than splitting the table.

## Alternatives considered

- **Per-source tables**. Identical schemas, twice the migration churn.
  Rejected.
- **Single-column `entity` + UUID-keyed `entities` table**. More
  normalized, but the values aren't UUIDs in any source — adding a
  layer of indirection costs a JOIN on every cursor read for nothing.
- **Hierarchical state per source as JSONB**. Loses the per-row
  index optimizations (especially `next_attempt_at`). Rejected.

## Notes

- Schema: `migrations/0005_backfill_state.sql`.
- Repository: `internal/core/infrastructure/storage/repositories/backfill_state_repository.go`.
- The auto-enable reset rule (refactoring_v2 §2.2) is implemented in
  `Upsert`: when `prev.Disabled && !s.Disabled`, ErrorCount and
  NextAttemptAt are wiped before persistence.
