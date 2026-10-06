# Historical Starter valuation

The first release supports one `Starter` option and one `USDT` payment per
launch. It values wallet transfers under that launch's historical list-price
policy. This is separate from the actual purchase payment and cost basis.

## Source and coverage

The MvKT `launchLedger` key-update history is durable historical evidence.
The reader walks every page in ascending update ID order, verifies the initial
`add_key`, and pins coverage two blocks behind the current head. It checks the
pinned block hash again after loading. Current `rwa_launches`, `updated_at`,
or a renewed cache TTL are never evidence of earlier conditions.

Purchase-counter updates are removed from the in-memory timeline; changes to
price, payment identity, status, pause flags, and sale dates remain. Terms that
change in the transfer's block return `same_block_change`, since block/time alone
does not establish operation order. Multiple launches, options, or currencies,
inactive sales, missing history, and unknown payment scales remain unavailable.

History reads have a 30-second overall deadline, at most 128 keys per launchpad,
512 rows per page, and 8192 total updates. Saturating the key/update budget is
an unavailable result, never a partial confirmed timeline. Concurrent readers
share one load; a snapshot is reused for 30 seconds. Failures are shared for
three seconds so an outage does not trigger a full retry per batch row. Coverage is stamped with
the pinned block, not the cache refresh time. A newly mined transfer may wait
for the next refresh before its block becomes covered.

## Automatic discovery and the shared connection

Historical primary valuation is always wired on service startup. Basenet is the
service's network; there is no separate enablement flag, network setting,
launchpad list, or payment-token JSON to maintain.

Launchpad contracts are discovered from the `launchpads` field of the
[Basenet contract allowlist](https://eqtz-admin.equiteez.com/equiteez-contract-allowlist-basenet.json).
Discovery refreshes with the history snapshot, so an allowlist change is picked
up without a redeploy. Missing/invalid discovery returns `allowlist_unavailable`;
it never silently keeps a removed launchpad.

Each historical launch version supplies its payment contract and token ID.
Decimals are read from that exact FA2 token's on-chain `token_metadata` key
history. MvKT exposes `token_info.decimals` as hex bytes: `36` decodes to `6`.
The reader verifies the initial `add_key`, loads all metadata-update pages under
the same pinned block, and uses the scale effective before the transfer block.
Explicit zero decimals are valid. Missing, malformed, deleted, future-only, or
ambiguous metadata remains unavailable; there is no six-decimal fallback.
Metadata changes also constrain the native price's effective interval.

Past payment generations are discovered from the launch history, including
contracts absent from today's `quote_tokens` allowlist. A missing old generation
does not hide another generation with confirmed metadata. At most 32 payment
identities are loaded, and metadata rows share the 8192-update budget.
The response records `mvkt_launch_ledger:onchain_token_metadata` provenance.

MvKT has one shared connection, with a usable Basenet default:

```yaml
mvkt:
  indexer_url: https://basenet.api.mavryk.network/v1
```

`MVKT_INDEXER_URL` optionally overrides this URL for a local or alternative
MvKT endpoint. Equiteez GraphQL keeps its own existing connection. Normal
Basenet deployment needs no new environment variables or activation step.

Historical USDT/USD rates reuse `token_prices` and `LatestRateAtOrBefore`.
The configured `server.fx_max_staleness_seconds` applies at the transfer's time;
missing or stale FX never yields a confirmed USD value. FX cache entries use
the exact instant, including negative lookups, and have a bounded lifetime and
size. They cannot share rates between different moments in one minute.

## API

`POST /v1/valuations/primary` inherits public RWA JWT authentication; the internal
listener has the existing trusted-pod posture. Maximum batch: 256 requests;
maximum JSON body: 1 MiB. Results preserve request order and echo identity/time.

```json
{"requests":[{"network":"basenet","contract":"KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP","token_id":"0","at":"2026-10-05T13:31:42Z","level":6635052}]}
```

A confirmed result includes `usd` as an exact decimal string, `terms`,
`fx_rate`, and `fx_at`. Terms include payment identity/scale, historical native
price, effective interval, launch identity, and chain coverage. An unavailable
result has `usd: null` and a bounded `reason`; failure of one evaluation does
not turn another result into zero. HTTP 400 denotes invalid requests; HTTP 200
can contain unavailable results when source data cannot be confirmed.

Monitor `primary_valuations_total{reason}`. Reasons distinguish history,
identity/metadata, unsupported conditions, ambiguity, and FX failures.

## Deployment and verification

Deploy external-data before deploying the backend consumer. Discovery and
metadata resolution run automatically on demand. Existing latest-price and chart APIs
retain their contract. Source failures make affected valuations unavailable.

CI uses controlled MvKT/FX fixtures. The public ANTH ledger fixture, captured
2026-10-06, confirms a native Starter price of 725 USDT for the three diagnostic
transfers at levels 6635052, 6642015, and 6645755, with payment contract
`KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v`, token ID 0. It also preserves the earlier
payment-generation changes. The companion USDT metadata fixture confirms
six decimals for all three generations through their on-chain records. Tests
exercise the automatic reader end to end. They do not assert a live USD rate
or deployment.

After deployment, check the control wallet's `pnl_24h`, percentage, and reasons
for remaining unavailable windows. Existing anchors and complete flow coverage
are still required; primary-aware anchor reconstruction is a separate stage.
