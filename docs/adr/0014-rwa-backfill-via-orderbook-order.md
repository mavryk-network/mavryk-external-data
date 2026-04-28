# ADR-0014: RWA backfill via `orderbook_order` event log

- **Status**: Accepted
- **Date**: 2026-04-28
- **Deciders**: backend team
- **Closes**: [rwa_backfill.md](../../rwa_backfill.md) (Stage 1)
- **Builds on**: [ADR-0008](0008-backfill-state-composite-key.md),
  [ADR-0012](0012-rwa-pair-discovery-and-normalization.md)

## Context

The Equiteez RWA collector ([equiteez_rwa.go](../../internal/core/infrastructure/jobs/equiteez_rwa.go))
polls **current orderbook state** every 10s and writes one bid/ask/last
snapshot per tick into `rwa_quote_prices`. That gives forward-only
history starting at deploy time. Anything older is invisible — backtests,
"show me trade history since June 2025", and freshly-deployed services
that need their first week populated are all blocked.

Live introspection of the indexer (`http://127.0.0.1:42007/v1/graphql`)
revealed an existing event log:

| Source | Rows | Suitable |
|---|---|---|
| `orderbook_order` | 917 | ✅ event log of every order ever placed |
| `dipdup_model_update` | 0 | ❌ DipDup audit-log: `audit: false` |
| `dodo_mav_history_data` | 0 | ❌ DODO MAV AMM not indexed in this deployment |

`orderbook_order` carries `created_at`, `ended_at`, `price_per_rwa_token`,
`order_type`, `fulfilled_amount`, `is_fulfilled` etc. — enough to derive
historical `last`-side prices from filled orders.

## Decision

**Stage 1: write one `last` PricePoint per filled order, forward-walk
the event log per pair, persist cursor in `backfill_state`.**

```
pair_id  = our pair_id (Tezos orderbook.address → rwa_pairs.id)
ts       = orderbook_order.ended_at
side     = "last"
price    = price_per_rwa_token / 10^quote_decimals
```

Stage 2 (replay bid/ask state from the full event stream) is **deferred**
until a real consumer asks for "historical bid/ask spread".

### Cursor design

Forward-walk by `orderbook_order.id ASC`, **not** by `ended_at`.
Reasoning:

- `id` is monotonic (Hasura BIGSERIAL).
- `ended_at` can have ties at second-level resolution within one block.
- Using `id` gives a tie-free cursor with no pagination tie-breaker logic.

This required adding a generic `cursor_id BIGINT` column to `backfill_state`
([0005_backfill_state.sql](../../migrations/0005_backfill_state.sql) — folded
into the original migration since the schema is pre-deployment). CoinGecko
backfill keeps using `oldest_ts` and leaves `cursor_id` NULL.

### Indexer ID caching

`orderbook_order.orderbook_id` is the indexer's internal integer FK; our
`rwa_pairs.id` is unrelated. Resolving address → indexer-id every batch
would burn a JOIN on the indexer. Instead, we cache it on
`rwa_pairs.equiteez_orderbook_id`
([0010_rwa_pairs_equiteez_id.sql](../../migrations/0010_rwa_pairs_equiteez_id.sql))
populated by `SyncRWAPairs` — same shape as the existing
`token_addr`/`orderbook_addr` cache.

Schema lives in [0002_lookup_tables.sql](../../migrations/0002_lookup_tables.sql)
(folded into the existing `rwa_pairs` definition; pre-deployment). A pair
without `equiteez_orderbook_id` (sync hasn't run yet, or indexer returned no
`orderbook.id`) is **skipped silently** by the backfill job — the next
successful sync fixes it.

### Caught-up state

When a per-pair fetch returns 0 rows, the pair is marked `disabled=true,
disabled_reason="caught_up"`. Distinct from `auto_disabled` (errors) and
`reached_floor` (CoinGecko hard floor) so dashboards render successful
completion clearly. Operators clear the flag manually to re-walk after a
long downtime.

### Idempotency

`rwa_quote_prices` PK is `(pair_id, side, ts)`. Re-running the backfill
upserts the same rows with the same values — no special dedup logic
beyond the cursor.

## Anti-decisions

- **Do not** synthesize bid/ask from per-order snapshots without full
  state replay. A naive "for every order, write a bid/ask at created_at"
  is *worse* than no data — it confuses the chart with values that
  weren't the best bid/ask in the book at that time.
- **Do not** use `created_at` or `ended_at` as the cursor (tie-breaking
  hell). `id ASC` is exact.
- **Do not** scrape on-chain via TzKT. The indexer has the data; building
  a parallel indexer is a non-goal.
- **Do not** add a separate `equiteez_backfill_state` table. The
  composite-key `BackfillStateRepository` (ADR-0008) already handles
  multiple sources; reuse > parallel infrastructure.

## Consequences

**Pros**

- Historical `last` prices become queryable via the existing
  `/rwa/prices` API the moment backfill runs.
- ~570 LOC, half-day implementation; no new operational surface (no
  new processes, no new tables, no new dashboards required).
- Stage 2 (bid/ask replay) inherits Stage 1 plumbing — same client
  query, cursor, pair mapping, idempotency.
- Forward cursor naturally bounds against new fills: live collector
  continues writing snapshots while backfill catches up.

**Cons**

- `last` only — bid/ask history requires Stage 2.
- Partial fills are written as a single point at `ended_at`; if an order
  filled in pieces over hours, the price between fills is invisible.
  Acceptable until indexer exposes per-fill events (currently
  `dipdup_model_update` is empty).
- `order_type` mapping (`1=buy`, `0=sell`) is unverified with the
  indexer team — for Stage 1 we don't write side metadata, so the worst
  case is a future Stage 2 needing to flip the mapping. Documented in
  [rwa_backfill.md §7.1](../../rwa_backfill.md).

**Operational**

- Disabled by default (`equiteez.backfill.enabled=false`); same opt-in
  posture as CoinGecko backfill.
- `cursor_id` is observable via `backfill_state` table; no Prometheus
  gauge specifically for it (the cumulative `job_rows_affected_total`
  histogram + `equiteez_backfill_saved` log lines already give ops
  enough signal).

## References

- [rwa_backfill.md](../../rwa_backfill.md) — full design (Stage 1 + Stage 2 sketch)
- [0008](0008-backfill-state-composite-key.md) — composite-key state repo
- [0012](0012-rwa-pair-discovery-and-normalization.md) — RWA pair discovery
