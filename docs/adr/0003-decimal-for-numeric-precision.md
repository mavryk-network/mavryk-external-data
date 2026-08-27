# ADR-0003: `shopspring/decimal` for monetary values

- **Status**: Partially superseded (wire format)
- **Date**: 2026-04-27
- **Deciders**: backend team

> **Supersession note (2026-08):** the storage/computation decision stands
> (decimal end-to-end, `numeric(38,18)` at rest), but the wire-format section
> is superseded: FT and RWA v1 endpoints serialise prices as **JSON numbers
> rounded to 6 decimal places** (see `docs/openapi.yaml`), not full-precision
> quoted strings. Ticker prices still serialise as full-precision strings.

## Context

The original entity layer mapped Postgres `numeric(20,8)` to Go `float64`.
For crypto prices this masks rounding errors silently:

- `0.1 + 0.2 != 0.3` in float64.
- A 22-significant-digit price quietly truncates on JSON round-trip.
- RWA yields and spreads compound the error: 0.0001% drift per write
  becomes a visible UI discrepancy after a week.

We considered keeping float64 (cheaper) vs going decimal at the boundary
(simpler, slower) vs going decimal end-to-end (most expensive but only
one mental model).

## Decision

Use [`github.com/shopspring/decimal`](https://github.com/shopspring/decimal)
end-to-end:

- DB column type widened to `numeric(38,18)` (covers wei-precision crypto
  and basis-point-precision RWA with headroom).
- `entities.TokenPriceEntity.Price` and `RWAQuotePriceEntity.Price` are
  `decimal.Decimal`. GORM scans/values via the type's built-in
  `sql.Scanner` / `driver.Valuer`.
- CoinGecko mapper builds `decimal.NewFromFloat` at the parse boundary
  (CoinGecko returns float64, no way around that).
- JSON serialization uses the decimal package's default (string output) —
  preserves precision at the API edge.

## Consequences

- ✅ No lossy float arithmetic anywhere on the hot path after the parse
  boundary. Tests can compare prices with `==` safely.
- ✅ `numeric(38,18)` is wide enough that we won't revisit this decision
  for any plausible asset class.
- ⚠️ ~10× slower than float64 for arithmetic. Not on the hot path
  (we don't compute on prices in-flight; we store and serve).
- ⚠️ JSON consumers must parse strings, not numbers. This is a deliberate
  API choice — clients that want lossy floats can `parseFloat()` themselves.
- 🔁 If a future feature needs hot-path decimal arithmetic at high QPS
  (e.g. live PnL), we'd cache derived float64s alongside, keeping
  `decimal.Decimal` as the source of truth.

## Alternatives considered

- **`float64` + `numeric(20,8)`** (status quo). Cheap but lossy. Rejected.
- **`int64` of "smallest unit"** (e.g. micro-USD). Common in trading
  systems but requires every consumer to know each token's exponent.
  Loses readability in logs / API responses. Rejected.
- **`big.Rat` / `math/big`**. Maximum precision but no fixed-point
  semantics — formatting requires explicit rounding rules at every
  boundary. More API noise than `decimal`. Rejected.

## Notes

- Schema columns: `migrations/0003_token_prices.sql`,
  `migrations/0004_rwa_quote_prices.sql`.
- Domain: `internal/core/domain/prices/point.go`.

## Addendum (2026-08): wire precision is hybrid, not fixed 6 dp

A flat 6-decimal wire grid destroyed sub-cent values: MVRK/BTC ≈ 6.8e-7
rendered as `0.000001` (+46%) or `0`, and a `delta_abs` of `0` then
contradicted a live `change_pct` on the same response.

FT and RWA prices now round to 6 decimal places at or above 0.01 and to 6
significant digits below it (`roundForWire`, handlers/rwa_prices.go). The
threshold is chosen so that every collected currency except btc and eth stays
byte-identical to the previous output for the configured tokens.

That byte-identity claim covers **prices**, not derived quantities. `delta_abs`
and `change_pct` have a magnitude of their own, so any move smaller than 0.01%
crosses the threshold and now renders with more significant digits instead of
flattening to `0` — deliberately, since a `delta_abs` of `0` beside a non-zero
`change_pct` was the defect. `change_pct` is also computed at a wider division
precision than decimal's default 16, which previously zeroed tiny-but-real
moves before rounding ever ran.

Ticker `change_24h_pct` and `share_pct` keep a flat 6 dp — they are quoted
strings on a different endpoint family, and their magnitudes never approach the
threshold.
