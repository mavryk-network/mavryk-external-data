# ADR-0013: Multi-currency RWA conversion is read-side, not stored

- **Status**: Accepted
- **Date**: 2026-04-28
- **Deciders**: backend team

## Context

RWA orderbooks on Equiteez quote prices in **one** native currency per
pair (USDT today; USDC/EURC plausible). Clients increasingly want to read
the same price in USD/EUR/AED/etc. for dashboards and portfolio
aggregation. Three options were evaluated:

- **A** — compute on read using FT-side `token_prices` as FX source.
- **B** — write derived rows into `rwa_quote_prices` at sync time.
- **C** — TimescaleDB continuous aggregate joining RWA × FX.

The native quote currency is **truth** (it's what trades settle in);
USD/EUR/AED equivalents are **presentation**. We have FX rates already:
`token_prices` carries CoinGecko's quotes for USDT/USDC/MVRK in N
currencies (USD, EUR, BTC, ETH, GBP, JPY, KRW, RUB, AED, CNY) at
minute cadence.

## Decision

**Approach A: read-side conversion on a query parameter.**

```
GET /v1/rwa/42/latest?in=usd,eur,aed
```

The handler:

1. Gets the native RWA snapshot from `rwa_quote_prices` (existing path).
2. Loads `pair.quote_symbol` from `rwa_pairs` (e.g. `USDT`).
3. For each target in `?in=`, calls `PriceConverter.Convert(quote_token,
   target, native_value, snap.ts)` — the converter looks up the latest
   `token_prices` row for `(quote_token, target)` and multiplies.
4. Returns a wrapped response with `native_quote`, the original `values`,
   and an `in.<target>` map for each requested currency.

Architecture pieces:

- `apiprices.PriceConverter` — interface in
  `internal/core/application/prices/converter.go`.
- `tokenFXConverter` — concrete impl over `LatestFXSource` (the existing
  `TokenPriceRepository.Query`); 60s in-process cache keyed on
  `(source, token, target, minute_bucket)`.
- Identity short-circuit when source token == target (e.g. `?in=usdt` on
  USDT-quoted pair) — `Rate=1`, `method=identity`, no upstream lookup.
- Per-target failure modes (`no_fx_rate`,
  `quote_currency_not_in_registry`, `unsupported_target`) surface as
  `in.<target>.fx.error` while the parent response stays 200. Stale FX
  (`now − rate.ts > fx_max_staleness_seconds`, default 300s) sets
  `fx.stale=true` plus a `fx_stale_responses_total{target}` metric.

Storage **does not change**. `rwa_quote_prices` keeps native-only.

## Consequences

- ✅ Adding a new target currency = 0 schema work; just needs presence
  in `prices.AllSupportedCurrencies` (already 10 entries).
- ✅ FX-source migration (e.g. CoinGecko → oracle) re-renders historical
  values automatically — derived data isn't frozen in storage.
- ✅ Backfill order independence: RWA and FT series are independent. If
  FT-backfill catches up later, historical `?in=usd` queries start
  succeeding without RWA-side rewrites.
- ✅ Snapshot-cache (`CachedRepository`, ADR-0007) still works — only
  the post-cache conversion step is per-request.
- ⚠️ Each `?in=` adds ~1ms / target on cache hit, ~5-15ms on miss. p95
  watch — refactoring_v2 §2 lists Approach C (CA) as the escape hatch.
- ⚠️ Spread-in-USD ≠ spread-in-USDT × FX(close), strictly: bid and ask
  use the same FX timestamp here, so percentage spread is preserved.
- 🔁 Move to Approach C if `?in=usd` becomes a de-facto default for UI
  AND p95 misses budget. API form stays.

## Alternatives considered

- **B (compute-on-write derived rows)** — rejected: ×N storage growth,
  RWA depends on FT backfill before it can write, FX-source pin baked
  into history.
- **C (continuous aggregate)** — deferred. Right shape only when latency
  becomes a problem; more invasive refresh-policy / migration work today.
- **Drop `?in=`, expect clients to do their own FX** — feasible but every
  client reimplements the same lookup; staleness/identity edge cases
  diverge. Centralization is cheaper.

## OHLC sub-decision (also resolved 2026-04-28)

When the future `/v1/rwa/:pair_id/ohlc` endpoint lands, candles will be
converted using **Variant A — close-of-bucket FX**: one rate per bucket
(latest ≤ `bucket_close`) multiplied across O/H/L/C uniformly.
`fx.method: "close_of_bucket"` advertises this in the response.

Reason: RWA pairs are quoted in stablecoins, so within-bucket FX
volatility is < 0.01% — the per-tick re-aggregation Variant B doesn't
buy meaningful accuracy at the cost of new continuous-aggregate
infrastructure.

## Notes

- API surface: `/v1/rwa/{symbol}` and `/v1/rwa/{symbol}/latest` accept
  the optional `?in=` query parameter. See
  [docs/openapi.yaml](../openapi.yaml) for `RWAExtendedSnapshot`,
  `ConvertedSnapshotBlock`, and `FXMeta` schemas.
  *(Path scheme later switched from `{pair_id}` to `{symbol}` —
  `{base}-{quote}`, e.g. `mars1-usdt`. Original wording kept for
  historical accuracy.)*
- Server config: `server.fx_max_staleness_seconds` (default 300),
  `server.max_in_currencies` (default 10).
- Metrics: `fx_conversion_duration_seconds`, `fx_conversions_total`
  (label `result`: success / identity / no_rate / unsupported_target /
  unregistered_source / query_error), `fx_stale_responses_total`.
