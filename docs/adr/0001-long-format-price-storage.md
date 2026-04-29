# ADR-0001: Long-format price storage in TimescaleDB

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

The original `quotes` table stored prices as a wide row:

```sql
CREATE TABLE quotes (
    token TEXT, timestamp TIMESTAMPTZ,
    btc, eth, usd, eur, gbp, jpy, cny, krw, rub  numeric(20,8),
    PRIMARY KEY (token, timestamp)
);
```

This shape exposed two structural problems:

1. **Adding a new currency = 6-point change**: domain struct, GORM entity,
   CoinGecko mapper, repository, migration, tests.
2. **No clean home for RWA**: orderbook prices are `(pair, side)` — the wide
   shape would either grow to dozens of columns (`mvrk_btc`, `mvrk_eth`, …,
   `pair123_bid`, `pair123_ask`) or fork into a parallel-but-different table.

Greenfield refresh: no production data to migrate, so the cost-floor for
"do it right" was zero.

## Decision

Adopt **long-format**: one row per (entity, source, ts, metric, value).

```sql
CREATE TABLE token_prices (
    token_symbol   TEXT, source_code TEXT,
    ts             TIMESTAMPTZ,
    quote_currency TEXT,             -- the metric
    price          NUMERIC(38,18),
    PRIMARY KEY (token_symbol, source_code, quote_currency, ts)
);
SELECT create_hypertable('token_prices', 'ts', chunk_time_interval => '7 days');
```

Same shape for RWA (see [ADR-0002](0002-two-domain-tables-ft-vs-rwa.md)).

## Consequences

- ✅ Adding a currency, source, or metric requires zero schema change — a new
  registry row + one literal in Go.
- ✅ Compression `segmentby = (token, currency, source)` is dramatically
  more effective on long-format than wide-format. 10–20× disk savings on cold
  chunks is realistic.
- ✅ Continuous aggregates (`token_prices_1h`, `_1d`) are trivially expressed
  with `time_bucket` — no per-currency hand-rolled views.
- ⚠️ Latest-snapshot queries need `DISTINCT ON (currency)` or a per-currency
  fan-out. We picked the former; it's fast on the latest-tail index.
- ⚠️ Row count grows ~Nx (8–10 currencies × samples). With TS compression
  the disk cost is comparable; query cost stays bounded by indexes.
- 🔁 If we ever need to write thousands of points per second for a single
  token, we'd revisit batching strategy — not the schema.

## Alternatives considered

- **Wide rows, JSONB `prices` column**. Lower migration cost, but JSONB
  can't be `segmentby`'d, breaks continuous aggregates, and forces every
  consumer to parse JSON for trivial reads. Rejected.
- **One table per token / per source**. Familiar in some warehouses but a
  TimescaleDB anti-pattern at scale. We'd lose hypertable-wide retention
  policies and operational uniformity. Rejected.
- **Status quo (8-currency wide table) plus a parallel RWA table**. Two
  models forever; every cross-cutting concern (caching, repository,
  metrics) duplicated. Rejected.

## Notes

- Worked design discussion lives in [docs/notes/opt-db.md](../notes/opt-db.md)
  (pre-ADR; informal).
- Schema migrations: `migrations/0003_token_prices.sql`,
  `migrations/0006_token_prices_aggregates.sql`,
  `migrations/0008_compression_retention.sql`.
