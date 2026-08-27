# feat(rwa): serve `quote_addr` for primary-market catalog entries

## Summary

`/v1/pairs/rwa` returned `quote_addr: null` for every launch-only asset — the launchpad has no orderbook currency row to take it from. But the address exists upstream: each sale-option payment carries its token (`launchpad_sale_option_payment.token`), and the sync already read exactly those rows to derive the base-tier price and the quote **symbol** — it just discarded the address sitting next to them.

```json
{"symbol":"khbe-usdt","market":"primary",
 "token_addr":"KT1FBJnqyY3EHB8dpwZ51gZqHBAXZifxwDnc",
 "quote_addr":"KT19bKTs9qsoBrspRNwsHn46YarEWuj3Vjc6",
 "orderbook_addr":null,"source":"equiteez"}
```

- **Migration 0018** — `rwa_launches.quote_addr TEXT` (nullable). Existing rows get NULL; the next hourly discovery tick fills it.
- **`BaseTierPrice()`** now also returns the **winning payment's** token address. All three served values — price, currency label, quote address — come from the same payment row: the schema allows several payment tokens per option, and mixing rows could pair the price with the wrong settlement token.
- **Preserve-on-empty upsert** — same contract as `rwa_pairs.quote_addr`: a degraded response (payment row without a token ref) does not wipe a previously-good address.
- **Catalog** — `primary` entries now carry `quote_addr`; `orderbook_addr` stays null by design (no orderbook escrow).

Additive; revises the earlier "quote_addr always null for primary" contract. openapi docs updated.

## Test plan

- [x] Unit: `BaseTierPrice` picks the max-price payment and its address travels with it (two tiers paying in *different* tokens); nil token-ref degrades to `""`
- [x] Handler: primary entry carries `quote_addr`; unsynced launch renders null, not `""`
- [x] Integration on live TimescaleDB (migrations 0001→0018): round-trip, preserve-on-empty, address-change propagation
- [x] `go build ./...`, `go vet ./...`, full unit suite, `-race` on `api/http/...` — green; `gofmt -l` clean
- [ ] After deploy + migration: first discovery tick populates `quote_addr` for KHBE/MCDX/XAUG

## Rollout

Run migrations, then deploy. No config changes. `quote_addr` is NULL for primary entries until the first post-deploy discovery tick (hourly).
