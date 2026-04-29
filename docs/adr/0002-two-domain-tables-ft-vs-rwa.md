# ADR-0002: Two domain tables (`token_prices`, `rwa_quote_prices`)

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

Once we picked long-format ([ADR-0001](0001-long-format-price-storage.md)),
the next question was whether FT-quotes (CoinGecko: token + currency) and
RWA-quotes (Equiteez orderbook: pair + side, with size) should live in the
same physical table or separate ones.

The shapes overlap:

```
FT:  (token, source, ts, currency, price)
RWA: (pair_id, source, ts, side,    price, size)
```

A unified table would carry a discriminator column and one nullable `size`,
plus polymorphic `entity_key` (TEXT to fit both `mvrk` and `42`).

## Decision

**Two physical tables**, one shape on the Go side.

```sql
CREATE TABLE token_prices    (token_symbol, source_code, ts, quote_currency, price);
CREATE TABLE rwa_quote_prices (pair_id, ts, side, price, size);
```

Both surface through the same `Repository` interface and `PricePoint`
domain type. A generic `Wrap[Req,Res]` HTTP adapter and a single
`CachedRepository` decorator serve both.

## Consequences

- ✅ Specific to each domain: RWA can carry `size`, FT cannot — no nullable
  columns "just in case." Schema reads naturally.
- ✅ Independent retention/compression/continuous-aggregate policies. RWA
  orderbook is bursty (sub-minute ticks); FT is a steady minute cadence.
  One table, one policy is wrong for both.
- ✅ Independent indexes. RWA latest-by-pair-side hits a different shape
  than FT latest-by-token-currency.
- ✅ Errant writes can't cross-contaminate (FK to `tokens` vs `rwa_pairs`).
- ⚠️ Adding a third source kind (e.g. oracle feed) means a third table —
  not "just another row." We accept this for clarity.
- ⚠️ Repository layer has two near-identical implementations. The
  duplication is real (~30%), but smaller than the cost of forcing
  unification on the SQL side. Generic helpers (refactoring_v2 §4.2)
  may pull this further down later.
- 🔁 If we onboard >5 source kinds, we'd revisit and consider a registry-
  driven schema-per-source pattern.

## Alternatives considered

- **Unified `prices(kind, entity_key, source, ts, metric, value, size)`**.
  Conceptually clean, but every query carries `WHERE kind = 'ft'` and
  RWA-specific operations (`size > 0`) become awkward. Compression
  `segmentby (kind, entity_key, metric)` works but is suboptimal vs.
  domain-specific keys.
- **Inheritance / partitioning by `kind`**. Postgres declarative partitioning
  works but doubles the operational surface (per-partition policies) without
  giving us the abstraction benefits we want.

## Notes

- Domain abstraction is in `internal/core/domain/prices/point.go`.
- Repositories: `internal/core/infrastructure/storage/repositories/{token_price,rwa_price}_repository.go`.
- Worked design notes: [docs/notes/rwa_pairs.md](../notes/rwa_pairs.md),
  [docs/notes/rwa_quotes.md](../notes/rwa_quotes.md).
