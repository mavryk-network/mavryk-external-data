# Equiteez recovery after a full indexer rebuild

Quotes keeps its catalog identities and price history while the Equiteez database
is rebuilt. CRM controls membership; indexer availability never changes the
operator's `enabled` flag. No frontend API changes are required.

## Configuration and rollout

1. Apply migration `0019_equiteez_recovery.sql` to the **quotes** database. It is
   repeatable and preserves existing rows, prices, cursors and manual disables.
2. Set `CONTRACT_ALLOWLIST_URL` to the same network-specific CRM JSON URL used by
   the indexer. This is required when RWA collection is enabled. The document
   must have `schema_version: 1` and explicit arrays `base_tokens`, `quote_tokens`,
   `orderbooks`, `launchpads`. Missing/null arrays, invalid contracts and unsupported
   schema versions are rejected. Explicit empty arrays are authoritative removals.
3. Enable `EQUITEEZ_BACKFILL_ENABLED=true` to recover trade history. The existing
   `EQUITEEZ_BACKFILL_START_FROM` floor limits the replay: configure it early enough
   to include the history that needs recovery.
4. Use `RWA_PAIR_SYNC_INTERVAL_SECONDS=60` (the new default), and set
   `EQUITEEZ_HEAD_MAX_AGE_SECONDS=300` or a suitable head-age tolerance for the chain.
   `EQUITEEZ_REQUIRED_INDEXES` defaults to token, orderbook, quote-token, super-admin,
   KYC and launchpad indexes. Override the comma-separated names for other deployments.
5. Stop the old quotes workers before starting the new workers. Old binaries still
   modify `enabled` and do not participate in the new advisory lock; do not mix them
   during rollout. The new binary needs migration 0019 before it starts.

## Recover the current incident

Legacy `enabled=false` does not record whether the cause was an operator or an
incomplete indexer response. The migration deliberately does not infer that intent.
Inspect these rows in the quotes DB and identify the contracts affected by this
incident:

```sql
SELECT id, token_addr, quote_addr, orderbook_addr, enabled, in_allowlist,
       indexer_generation, last_synced_at
FROM rwa_pairs WHERE source_code='equiteez' ORDER BY id;
SELECT token_addr, launchpad_addr, enabled, in_allowlist, indexer_generation,
       last_synced_at
FROM rwa_launches WHERE source_code='equiteez' ORDER BY token_addr;
SELECT entity_key, disabled, disabled_reason, indexer_generation,
       cursor_ts, cursor_id, error_count, last_error, next_attempt_at
FROM backfill_state WHERE source_code='equiteez';
```

Set these explicit comma-separated recovery lists on the first deployment:

```dotenv
EQUITEEZ_RECOVER_ORDERBOOKS=<affected orderbook contract addresses>
EQUITEEZ_RECOVER_LAUNCH_TOKENS=<affected primary token contract addresses>
```

Only a contract present in the CRM allowlist and discovered in the current ready
indexer generation can be recovered. Each successful recovery is recorded in
`equiteez_recovery_applied` in the same transaction as re-enabling the row. Restarting
the service, keeping the variables set, or later manually disabling that contract
does not re-enable it again. Remove the variables after recovery. Existing manual
disables not selected for recovery remain untouched.

Legacy `backfill_state.disabled_reason` values `auto_disabled` and `caught_up`
resume automatically for eligible pairs after upstream readiness is verified.
`manual` and `reached_floor` remain stopped; inspect their reason before changing them.

## Runtime behavior

- `enabled` is operator-owned. `in_allowlist` is CRM-owned. A pair requires its base,
  quote and orderbook contracts; a launch requires its base, payment and launchpad
  contracts. Reads/collection require both flags. Delisting and re-listing preserve
  the local pair ID, history and manual switch. Legacy rows with an unknown quote
  or launchpad address retain their previous membership until discovery resolves
  it; an explicitly empty CRM relationship list still delists them.
- A successful CRM snapshot is persisted in `equiteez_allowlist`. On CRM HTTP or
  validation failure the last valid snapshot for that exact URL is reused, including
  after process restart. Without such a snapshot discovery waits. Cached membership
  does not expire automatically: an outage must not erase the catalog. Monitor the
  snapshot's `fetched_at` and the `equiteez_allowlist_using_persisted_snapshot` log.
  CRM uses at most one third of the remaining sync deadline, leaving time for
  cached fallback and indexer reconciliation even if CRM hangs.
- The indexer's `dipdup_schema(name=public).created_at` identifies a full DB rebuild.
  All required indexes must be present and realtime; existing extra indexes must
  also be realtime; datasource heads must be fresh. Index levels are not compared
  to the chain tip because they may reflect the last matching operation.
- Catalog, live quotes and each history batch check readiness/generation before
  and after reading upstream. A changed generation discards the fetched snapshot.
  Partial catalog responses never remove saved entries. A pair awaits discovery
  in the new generation before collection/backfill uses its refreshed internal ID.
- Saved API data remains readable with its original timestamps while the indexer
  is unavailable. No intermediate replay state is recorded as a current quote.
- Backfill checkpoints persist the generation. A changed or initially unknown
  generation resets the `(ended_at,id)` cursor once and replays from `start_from`.
  Existing history is upserted on `(pair_id,side,ts)`; IDs and history are not deleted.
  Retries use bounded exponential backoff; `EQUITEEZ_BACKFILL_MAX_ERRORS` no longer
  permanently disables Equiteez history. Invalid quote configuration still stops
  the affected pair explicitly.
- Each history step processes at most one UTC day of fills, then checkpoints
  only after all 1m/1h/1d candle refreshes succeed. Later fills in the same fetched
  page are re-read from that checkpoint on the next tick; empty days are skipped.
  Each refresh statement spans at most one UTC day, keeps `statement_timeout`,
  and reports the view and requested window on failure. In-progress buckets stay
  in the real-time union. Extension-probe errors also retry;
  temporary DB errors are never cached as absence of TimescaleDB. A PostgreSQL
  advisory lock serializes new Equiteez workers across replicas. Contending workers
  retry within their operation deadline and release pool connections between
  attempts, so aligned schedules cannot continually skip catalog sync. Configure
  a DB connection pool of at least
  two connections (one lock holder plus the worker connection; default is 25).

This generation detector covers a dropped/recreated indexer database. Restoring
an old backup with the same schema creation timestamp requires explicit checkpoint
reconciliation. Trade replay reconstructs `last` prices; bid/ask snapshots that
were never collected during downtime cannot be recovered from order fills.

## Verification

```sh
go test -race -short ./...
go test -race -timeout 15m -tags=integration ./tests/integration/...
```

The integration scenario uses an isolated TimescaleDB and fake CRM/Hasura to cover
outage, a new empty/partial indexer generation, delayed discovery, changed numeric
IDs, persisted CRM fallback, manual disables, one-time recovery, real delisting and
re-listing, transient history failures, legacy stopped backfills and history retention.
Regression cases also cover stalled CRM requests, failed TimescaleDB probes, legacy
unknown relationships, and generation changes during catalog/history reads.
Check `equiteez_catalog_synced`, `equiteez_live_waiting_for_indexer`, and
`equiteez_backfill_saved` logs after deployment. Verify restored assets and series
through the RWA API. Empty trending remains valid when actual recent volume is zero.
