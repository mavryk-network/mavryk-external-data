# ADR-0015: Charts API — line / OHLC / OHLCV for FA and RWA

- **Status**: Proposed
- **Date**: 2026-05-01
- **Deciders**: backend team
- **Drives**: [charts.md](../../charts.md) (full plan, with stage breakdown)
- **Builds on**: [ADR-0006](0006-generic-handler-wrap.md),
  [ADR-0013](0013-multi-currency-rwa-conversion-read-side.md),
  [ADR-0014](0014-rwa-backfill-via-orderbook-order.md)

## Context

Frontend wants time-series charts over both inventories — FA tokens
(`/v1/prices/{token}`, CoinGecko-fed) and RWA pairs
(`/v1/rwa/{symbol}`, Equiteez-fed). Three classes of chart: line/series,
OHLC candlesticks, and OHLCV (volume).

Storage state at the time of this ADR:

| Class | Hypertable | 1h CA | 1d CA | 1m CA | Volume |
| --- | --- | --- | --- | --- | --- |
| FA tokens | `token_prices` ✅ | ✅ | ✅ | ❌ | ❌ |
| RWA pairs | `rwa_quote_prices` ✅ | ✅ (no volume) | ❌ | ❌ | ❌ (only top-of-book `size`) |

The two classes also differ in how multi-currency works:

- **FA**: `token_prices` already keyed by `quote_currency` — every supported
  currency is its own row. Reads filter `WHERE quote_currency=?`.
- **RWA**: stored only in native quote (USDT). Multi-currency for the
  existing endpoint runs on read-side via `PriceConverter` (ADR-0013) —
  one FX-rate per tick.

## Decision

### 1. Symmetric URL contract across classes

| Class | series | ohlc | ohlcv (TODO) |
| --- | --- | --- | --- |
| FA | `/v1/prices/{token}/series` | `…/ohlc` | `…/ohlcv` → 501 |
| RWA | `/v1/rwa/{symbol}/series` | `…/ohlc` | `…/ohlcv` → 501 |

Same DTO shape, same intervals (`raw / 1m / 5m / 15m / 1h / 4h / 1d`),
same error model — only the class-specific bits (currency parameter,
symbol parsing) differ.

### 2. OHLCV is parked, returns 501 NOT_IMPLEMENTED

Both classes lack honest volume:

- **RWA**: `rwa_quote_prices.size` is a top-of-book snapshot polled every
  ~10s — not traded volume. Aggregating it into `vb/vq` would mislead the
  frontend. Real traded-volume requires ingestion from `orderbook_order`
  (ADR-0014 Stage 2 territory).
- **FA**: CoinGecko `/simple/price` (current ingestion) doesn't return
  volume at all. A correct path is migration + switch to
  `/coins/{id}/market_chart/range` + CA recreation + backfill.

Each is a multi-day project orthogonal to the chart API itself. We do not
block the line/OHLC release on either. The `/ohlcv` URL is registered
from day one with a 501 stub — frontend can build against the final URL
contract; contract tests assert the exact 501 code/body so the endpoint
cannot silently regress to a partial-truth implementation.

Wire body for the stub (frozen):

```json
{
  "code": "OHLCV_NOT_IMPLEMENTED",
  "message": "OHLCV is not yet available; track Stage 4 in charts.md"
}
```

### 3. CA layout — three stored, six served

For both classes we materialise 1m / 1h / 1d as continuous aggregates;
5m, 15m, 4h are derived on-demand by re-bucketing inside the repository
(`time_bucket('5 minutes', bucket)` over `_1m`, etc.). Three CAs cover
six intervals at the cost of one extra `GROUP BY` for 5m/15m/4h reads.

Direct reads of the tick hypertable are excluded from the chart hot
path — the `_1m` CA is what makes chart-handler logic identical between
FA and RWA repositories (no special hypertable-fallback branch for FA).

### 4. Multi-currency

- **FA**: native, runs in SQL. The existing `quote_currency` key extends
  cleanly into the chart CAs since they already group by it. No FX layer
  for FA charts.
- **RWA**: opt-in `?in=` (≤1 currency). Per `rwa_quotes_adds.md` Variant A,
  one FX-rate per candle, applied to all four price fields at
  `bucket + interval` (close-of-bucket). This keeps the candle valid
  (`l ≤ o,c ≤ h`) and bounds the FX-lookups to O(N candles). Wired in
  Stage 3 of charts.md; rejected as `400` until then.

### 5. Application layer (Stage 0 of charts.md, this PR)

```
internal/core/application/prices/charts.go
  Interval (enum) + ParseInterval + AllChartIntervals
  CandleQuery, Candle (volume nullable)
  CandleRepository interface           — satisfied by RWA/FA repos in Stage 1/2
  ChartService { Repo, Converter, Caps, MaxLimit }
    .Series  → projects close-price
    .OHLC    → passes candles through
    .OHLCV   → ErrOHLCVNotImplemented (Stage 4)
  DefaultCaps + ValidateRange          — per-interval window cap (charts.md §2.2)

internal/core/api/http/handlers/charts_common.go
  ChartEnvelope, SeriesDTO, OHLCDTO, OHLCVDTO   — wire shapes
  ParseChartInterval                            — 400 on missing/unknown
  NotImplementedOHLCV                           — gin stub for the 501
```

Repository implementations and route wiring belong to Stage 1 (FA) and
Stage 2 (RWA).

## Anti-decisions

- **No bid/ask/mid on charts** (RWA-only). `last` only. Storage retains
  bid/ask/mid for a future spread-analysis endpoint, not for
  charts-with-`?side=`. Multi-side complicates the candle invariants
  without product value at this stage.
- **No multi-currency per chart request**. `?in=` capped at one. Several
  target currencies in one response would 4× the FX work and bloat
  payloads — fronts that need 2+ currencies make 2+ requests.
- **No on-demand aggregation from raw ticks on the hot path**. Every
  served interval comes from a CA (or CA re-bucket). This was the call
  for FA too — adding `token_prices_1m` keeps the FA repository symmetric
  with RWA, eliminates a hypertable-scan branch.
- **No pre-computed Redis cache for charts**. CAs already serve that
  role; layering Redis on top would double the invalidation surface for
  no measurable read latency win.
- **501 over 404 for `/ohlcv`**. 501 says "endpoint exists, not yet
  implemented", giving frontend a stable URL to render a "coming soon"
  state. 404 would lie about the URL plan.

## Consequences

**Pros**

- One `ChartService` for both classes; class-specific bits live in tiny
  per-class handlers (`rwa_charts.go`, `token_charts.go` in Stage 1/2).
- Three CAs per class → six intervals served. ~10k-row cap per request
  on hot intervals.
- 501 stub commits to the URL contract early — frontend can build ahead
  of Stage 4 without speculative paths.
- OHLCV honesty: no fake volume on the wire; explicit code,
  contract-tested.
- Every interval comes from a CA — no `time_bucket()` over hypertables
  on the hot path.

**Cons**

- The existing `rwa_quote_prices_1h` CA must be drop+recreated to add
  volume columns in Stage 4. A small ops dance, but on continuous
  aggregates this is routine.
- FA `1m` at the current 60s polling cadence yields ~1 sample per
  bucket. Valid (open=high=low=close), and migration-friendly when we
  raise the cadence in Stage 4, but degenerate today.
- Stage 4 has two parallel sub-tracks (RWA traded-volume, FA volume
  ingestion) that lift the 501 separately — coordinate releases or
  the two `/ohlcv` paths will go live at different times.

**Operational**

- New migrations: `0010_rwa_candles.sql` (Stage 2), `0011_token_prices_minute.sql`
  (Stage 1), `0012_token_prices_volume.sql` (Stage 4). API versions
  to `v1.2.0` once Stage 3 ships.
- `chart_query_duration_seconds`, `chart_query_rows`,
  `chart_query_cap_hits_total` Prometheus series added with the handler
  routes. Reuse existing per-IP RPS limit; profile before adding a
  charts-specific bucket.

## References

- [charts.md](../../charts.md) — full proposal with stage breakdown and SQL
- [ADR-0013](0013-multi-currency-rwa-conversion-read-side.md) — multi-currency RWA
- [ADR-0014](0014-rwa-backfill-via-orderbook-order.md) — `orderbook_order` ingestion (Stage 4 builds on this)
- [rwa_quotes_adds.md](../../rwa_quotes_adds.md) — Variant A FX-in-candle design
