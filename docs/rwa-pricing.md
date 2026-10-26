# RWA pricing for wallet consumers

`GET /v1/rwa/{base}-{quote}/latest` and the existing raw list endpoint
`GET /v1/rwa/{base}-{quote}` include an additive nested `pricing` object.
There is no separate pricing endpoint. Existing numeric top-level prices,
converted currency keys, and the legacy `primary_issuance` block retain their
meanings. Current pricing always attempts USD conversion without `?in=usd`.
A known pair before its first trade now returns 200 with legacy `price: null`
and `timestamp: null`, alongside the new pricing facets. Latest-mode list
returns the same single observation (without latest-only statistics).

| Field | Meaning | Price timestamp / FX time |
|---|---|---|
| `market_price` | Midpoint of the best active limit bid and ask from one complete observation | `observed_at` |
| `last_sale` | Contract's last matched price; requires `last_matched_price_timestamp` | Actual `executed_at` |
| `primary_issuance` | Active primary sale's undiscounted list price | Launch catalog sync time |

All monetary fields inside `pricing` are exact decimal **strings or null**. `status` describes
the native price; `fx_status` separately describes USD conversion. A valid
native quote with unavailable/stale historical FX has `usd: null`, never zero.
An empty/one-sided/crossed/stale book has no midpoint. No fallback replaces it
with last sale, launch price, or an invented PMM price. The collector selects
one best limit order per side in the same GraphQL request, excluding market
orders (including their protection prices), canceled, refunded, fulfilled,
expired and zero-remainder orders. It checks wall-clock expiry at collection and
read time. Each market quote expires at the earlier of its observation TTL and
the selected orders' deadlines. Null order expiry means no deadline, matching
the indexer's depth view. Legacy historical bid/ask points retain their stored
contract-price semantics.

For a wallet holding value, select an explicitly agreed valuation basis and
multiply its available USD per-token price by the normalized token balance.
Do not mix wallet position values with the value of the whole token issue.
Description and issuer metadata are outside this service's pricing scope.

## Identity and freshness

Scope is the existing enabled, CRM-allowlisted **basenet token-ID-0** catalog.
Validate `network`, `token_address`, `token_id`, `quote_address` and
`quote_token_id`; a symbol is a discovery key, not sufficient identity proof.
The collector requires an explicitly present token ID on both token references,
one resolved payment currency, and a quote contract/symbol matching the catalog.
Only the indexer's exact null-token `USD` sell-order label is ignored; it is not
a payment identity. Other unresolved or multiple payment currencies fail closed. Nonzero,
missing or ambiguous identities yield unavailable quotes, not another token's
price. Quote decimals use the existing registered currency metadata.

Consumers must recheck each facet's `valid_until` when using cached results.
Primary and secondary observations can have different expiry times on an asset
that has both. A fresh matching primary launch may remain useful before the
first secondary snapshot is collected. Unknown secondary identity is filled
from that verified launch only when there is no contradictory observed ID.

The collector validates indexer readiness/generation before and after reading.
The API compares the saved generation to the catalog and rejects future or
expired observations. `rwa.quote_max_age_seconds` / `RWA_QUOTE_MAX_AGE_SECONDS`
defaults to 180 seconds. `rwa.network` / `RWA_NETWORK` defaults to `basenet`;
other networks are currently rejected. Configure the collection and catalog
sync intervals below the freshness limit.

Read APIs do not call the indexer. When collection fails or an indexer rebuild
begins, the previous observation expires at its existing deadline. Generation
checks plus expiry bound freshness; they are not instantaneous reorg detection.
An old executed trade may still be the most recent sale. A *stale observation*
retains that historical sale with `status: stale` and `reason: snapshot_stale`;
consumers must not call it a current confirmed last sale.

## Historical rows

Windowed raw history (`?from=...&to=...`) enriches each existing last-price row
only when its original database timestamp and price exactly match a recorded
strict observation. Joins retain subsecond precision even though the legacy
`timestamp` display has second precision; `pricing.observed_at` retains the
precise time. Snapshots are loaded in one batch, never joined to today's latest
record or a nearby observation. Old/backfilled/mutated unmatched points have
`pricing: null`. No predeployment history is invented. Optional current/archive read failures
also yield `pricing: null`, with a logged diagnostic, without suppressing healthy
legacy price results. Snapshot/archive write failures likewise report a job
error while allowing the legacy price writer to continue.

Historical market availability is evaluated at capture time. Historical USD is
opt-in using `?in=usd` (or a currency list containing usd), reusing the existing
conversion request. Without that target, native facets remain available and
USD is null with `fx_status: not_requested`, without additional FX lookups.
When requested, USD uses observation time for market price and actual execution
time for last sale. Current latest-mode pricing always attempts USD.
An observation from an earlier indexer generation remains recorded history;
network/contract/token/quote/orderbook identity is still validated. The live
path continues enforcing the current catalog generation. These observations do
not promise reorg finality. Historical `pricing.primary_issuance` is always null:
a current launch catalog record is not evidence of that launch's earlier state.

The raw history remains the existing last-price timeline. Primary-only and
before-first-trade assets do not gain invented historical rows. Series and OHLC
continue aggregating legacy last prices. Complete strict observations are
archived from deployment onward, with 7-day compression and 2-year retention
when TimescaleDB is installed, matching existing raw-price retention. Plain
PostgreSQL installations need external retention maintenance, as for legacy
price tables.

## Rollout

1. Apply migrations `0025_rwa_market_snapshots.sql` and
   `0026_rwa_market_observations.sql` using the normal migration runner.
2. Deploy the service. The existing collector writes one complete latest row
   per pair, including NULL sides when a side disappears, and atomically appends
   the same complete observation to history. No legacy historical data is rewritten. Fresh launch sync verifies the primary payment ID; existing launch
   rows have a NULL `pricing_quote_token_id` until that sync succeeds.
3. Verify nested `pricing` on `/latest` for known secondary and primary assets,
   and on a newly collected raw historical point, then deploy consumers. Before first collection, a known pair
   returns explicit `snapshot_missing`, without disturbing legacy quotes.

No cache flush or price-history backfill is required. Current snapshot records are
bounded by catalog size, while the archive has the retention policy above; historical quote caches remain
independent. Future new network or nonzero-token support requires an explicit
identity design, not removal of the current validation guards.
