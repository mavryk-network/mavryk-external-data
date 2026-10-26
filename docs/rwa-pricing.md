# RWA pricing for wallet consumers

`GET /v1/rwa/{base}-{quote}/pricing` supplies three separate pricing facets.
It has the same authentication as existing RWA routes. It always attempts USD
conversion and does not require `?in=usd`. The existing `/latest`, historical
prices, series and candles retain their existing behavior.

| Field | Meaning | Price timestamp / FX time |
|---|---|---|
| `market_price` | Midpoint of the best active limit bid and ask from one complete observation | `observed_at` |
| `last_sale` | Contract's last matched price; requires `last_matched_price_timestamp` | Actual `executed_at` |
| `primary_issuance` | Active primary sale's undiscounted list price | Launch catalog sync time |

All monetary fields are exact decimal **strings or null**. `status` describes
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

## Rollout

1. Apply migration `0025_rwa_market_snapshots.sql` using the normal migration runner.
2. Deploy the service. The existing collector writes one complete latest row
   per pair, including NULL sides when a side disappears. No historical data is
   rewritten. Fresh launch sync verifies the primary payment ID; existing launch
   rows have a NULL `pricing_quote_token_id` until that sync succeeds.
3. Verify `/pricing` for a known secondary and a known primary asset, then deploy
   consumers of the additive endpoint. Before first collection, a known pair
   returns explicit `snapshot_missing`, without disturbing legacy quotes.

No cache flush or price-history backfill is required. New snapshot records are
bounded by catalog size and are read directly; historical quote caches remain
independent. Future new network or nonzero-token support requires an explicit
identity design, not removal of the current validation guards.
