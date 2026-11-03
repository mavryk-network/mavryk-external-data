# RWA supply and market capitalization

The service owns supply, current valuations and market-cap history. Inputs are
CRM contract allowlist, MvKT and the existing price/FX collectors. It does not
call the RWA backend or accept supply uploads. One instance serves the network
configured by `rwa.network`.

## Policy and API

```
circulating = (total_raw - unique associated launchTreasury balances) / 10^decimals
market_cap  = circulating * price
fdv         = total_raw / 10^decimals * price
```

FDV uses issued total, not a maximum supply. Closed/paused campaign treasury
balances remain excluded; orderbook escrow remains circulating. A confirmed
zero is valid. Missing scope, identity, scale, price or FX is not zero.

Secondary pair presence selects its `last`, even if no trade/last is available.
There is no primary fallback for such a pair. Without a secondary pair, the
existing supported Starter/USDT primary history supplies its recorded list
price. Listing valuation ignores sale eligibility; transfer/holdings/PnL still
enforce their original active-sale policy. Unsupported options/payment metadata
remain unavailable. Sale status and pause state are exposed separately.

```
GET /v1/rwa/test-usdt/valuation/latest?in=usd,eur
GET /v1/rwa/valuations/latest?limit=100&in=usd,eur
GET /v1/rwa/test-usdt/series?metric=market_cap&interval=1h&in=eur&from=2026-11-03T10:00:00Z&to=2026-11-03T11:00:00Z
```

All routes inherit existing RWA auth/rate limits. No `network` query is needed.
Latest/batch return exact decimal strings; revision is a monotonic BIGINT encoded
as a decimal string, scoped to `(network,contract,token_id)`. Compare revisions
as integers, never floating point. Each independent quality has absolute
`fresh_until`/`expires_at`; read/cache time must not renew these deadlines.
Batch paginates identities, includes all known quotes and keeps delisted current
identities as invalidations. Its capped `errors` diagnostics also expose assets
whose identity could not be resolved; unknown IDs are never defaulted to zero.

Charts reuse `SeriesDTO`/`SeriesPointDTO`. `p` is native quote; `in=eur` adds `eur`
without changing `p`. Numbers are for chart display. `p:0` and `p:null` differ.
`t` is bucket start, `as_of` is the selected observation. Market-cap FX is saved
on that observation's `as_of`; 1m/1h/1d reuse exactly those inputs. Failed EUR
does not erase native cap or USD. Currency quality and gaps identify the loss.
`currency` is omitted for cap. Missing metric/`metric=price` retains existing
price behavior, including currency and FX at bucket close. OHLC is unchanged.

OpenAPI is in [openapi.yaml](openapi.yaml). Executable backend-consumer fixtures:
[latest](../internal/core/api/http/handlers/testdata/rwa_valuation/latest.json),
[batch](../internal/core/api/http/handlers/testdata/rwa_valuation/batch.json),
[series](../internal/core/api/http/handlers/testdata/rwa_valuation/series.json).

## CRM scope and source evidence

The existing catalog scheduler is the only CRM refresh path. It now refreshes
even when orderbook collection is disabled or the Equiteez indexer is not ready.
Catalog, supply and primary share the persisted reader. CRM failure keeps the
last validated document/version/fetched_at, records `crm_unavailable`, and does
not turn an unknown list into an empty one. Scope changes invalidate the primary
history cache; there is no separate hardcoded URL or four-launchpad limit.

Supply checks all launchpads ever approved in this CRM source up to the scope
version. Deleting a launchpad is not evidence that its treasury balance is zero.
Its on-chain membership/balances must remain readable; otherwise circulating cap
is unavailable while independently proven total/FDV can remain available.
Historical scope starts at the first persisted CRM observation, not listing time.
An A → B → A change has a distinct scope version. The source URL is part of the
history boundary: keep the configured URL stable; migrating CRM URLs requires
preserving the corresponding scope evidence, not silently dropping old addresses.

All supply reads use one lagged block and verify its hash again after the input
reads. Metadata uses that block's `token_metadata` pointer and inline decimals.
External metadata URIs are not assumed to be historical proof. A missing inline
scale returns an explicit metadata reason. A changed scale is quarantined with
`expected_decimals`; repeated polls cannot silently accept the new scale.
Restoring the previous trusted scale recovers automatically. A legitimate scale
change requires an explicit reviewed data correction/migration preserving the
history of that change; do not delete revision/current rows to bypass it.

## Rollout and rollback

1. Apply migration `0027_rwa_valuations.sql` with the existing migration runner.
   Deploy the API/collector code with `RWA_SUPPLY_ENABLED=false` (the default).
2. Confirm the configured CRM source and MvKT chain ID. Basenet defaults to
   `NetXmtMsNf69w1w`; other networks need an explicit expected chain ID and do
   not gain new primary-protocol support from this feature.
3. Enable `RWA_SUPPLY_ENABLED=true`. Default cadence is 60s, block lag 2, with a
   separate supply advisory lock and bounded concurrency/HTTP/response budgets.
   The existing catalog refresh interval controls CRM discovery latency.
4. Inspect latest/batch qualities, errors, available_from and gaps. Monitor
   `rwa_supply_collections_total{outcome}`, `rwa_supply_lag_seconds`, and
   `rwa_supply_collection_coverage_ratio`. Labels never contain asset addresses.
   The coverage metric measures successful collection, not monetary usability;
   use API qualities to assess price, metadata and FX coverage.
5. Compare with the backend on the **same block, decimals, treasury scope and
   price inputs** before switching its consumer. Current-time comparisons can
   differ because treasury transfers and price observations occur independently.

Stopping the collector leaves latest data to age using its original deadlines;
charts remain readable. Roll back enablement before rolling back code. Do not
drop history/current/revision tables, reset revisions or clear the backend cache
to simulate recovery. Coordinate removal of the new API with its consumers.

Expected migration differences from legacy display `/latest`: strict supply/quote
identity and inline asset-scale proof can make monetary valuation unavailable
while legacy numeric display still exists. Recorded primary listing prices remain
usable for cap when paused/closed. Secondary valuation never substitutes midpoint.
These are explicit quality differences; inspect reasons rather than coercing null.

## Bounded historical reconstruction

The production image includes `/app/rwa-valuation-backfill`. The equivalent local
command, using the same config/environment/database as the service, is:

```sh
go run ./cmd/rwa-valuation-backfill \
  --job rwa-test-20261103 \
  --contract KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP --token-id 0 \
  --from-level 100000 --to-level 100400 --step 4 \
  --max-steps 100 --timeout 10m
```

The explicit token ID is verified. Each run is bounded to 1–1000 samples and at
most 30 minutes; per-asset timeout/response caps still apply. `step` is in blocks,
not seconds. Repeat the same arguments/job ID to resume the durable checkpoint.
Changing inputs under an existing job ID is rejected. Transport/database failures
leave the step retryable. Missing historical evidence advances with a durable
reason and appears in the printed coverage report; use a new job ID to retry a
previously completed gap range after adding evidence. `complete` means the
requested range was processed, not that all valuations are available. `recorded`
counts samples with usable native cap; per-currency quality remains independent.

Backfill reads historical storage pointers, balances, metadata, persisted CRM and
recorded market routing, and uses price/FX at or before the evaluation time. It
does not apply the live-head age test to old blocks. It cannot establish routing
before the first recorded routing evidence or beyond that evidence's supply
freshness budget (15 minutes by default); those periods report
`routing_unverified`. No pre-deployment synthetic chart is created from today's
supply or catalog. Reconstructed data cannot replace direct observed evidence.
Writes are idempotent and checkpointed after publication; repeated runs refresh
affected aggregate buckets using a separate durable refresh watermark. Backfills
serialize with each other using the existing advisory-lock mechanism; their lock
does not block live supply or price collection. A proven reorg invalidates affected observations and
their chart closes; gaps remain until valid replacement evidence exists.

Raw supply/valuation rows are compressed after 30 days and retained while
aggregate close IDs refer to them. Do not attach a raw drop policy: it would
remove chart provenance. Current revision rows and CRM/checkpoint history are
durable. The existing market-observation retention is two years; older backfill
may lack price evidence even if chain data is still available. Complete history
is only what reported coverage proves, not an assumed two-year guarantee.

## Implementation and verification

The moved MvKT supply algorithm/parser tests remain the source for exact nat,
identity, treasury deduplication, historical pointers, mint/burn/killed and reorg
rules. New helpers cover missing responsibilities only: persisted scope history,
decimal valuation/quality composition, atomic revision/CAS, line aggregate reads,
bounded reconstruction/checkpointing and HTTP projection. Primary separates
listing price from purchase eligibility inside its existing history reader.
FX uses the existing `PriceConverter`; no second CRM/MvKT/FX client, price
collector, backend callback, OHLC implementation or validation framework was added.

```sh
go test -race ./...
go test -race -tags=integration ./tests/integration/... -timeout=15m
go vet ./...
make openapi-lint
```

Integration tests apply all migrations on the pinned TimescaleDB image and cover
collector inputs, CAS/concurrent writers/retry, revisions above 2^53, reorg,
metadata conflict, preserved treasury scope, partial FX, closing nulls,
partial-bucket boundaries, observation-time FX and resumable backfill gaps.
