# ADR-0012: RWA pair discovery from Equiteez allowlist + price normalization via `tokens.decimals`

- **Status**: Accepted (sync cadence superseded)
- **Date**: 2026-04-28
- **Deciders**: backend team

> **Supersession note (2026-08):** discovery is no longer a one-shot startup
> sync. `RWAPairSyncJob` re-reads the allowlist on an hourly ticker
> (`rwa.pair_sync_interval_seconds`), soft-disables pairs/launches that left
> the allowlist (`disabled_reason='sync_missing'`, auto-re-enabled on
> reappearance), and new listings need no restart.

## Context

Two related questions about the RWA-side of the service surfaced in
operations:

1. **Where does the list of RWA pairs come from?** Originally, operators
   were expected to hand-INSERT rows into `rwa_pairs` (token contract,
   orderbook contract, base/quote symbols). For 100+ pairs across
   Equiteez tranches this doesn't scale; pairs come and go on the
   indexer side, and a manual sync drifts within hours.

2. **In what units does the indexer report prices?** Equiteez returns
   orderbook prices in the **smallest unit of the quote currency**
   (e.g. micro-USDT: `56_250_000` = 56.25 USDT), normalized to "1 whole
   base token". Naively storing the raw value writes 10⁶× the correct
   price into `rwa_quote_prices.price`.

The pre-rewrite code had a one-off helper
`NormalizedUSDPerTokenFromOrderbook` with a hardcoded
`if quote == "USDT" { /= 1_000_000 }`. That solved (2) for the only
quote currency in scope at the time but didn't generalize — and it
addressed nothing about (1).

## Decision

### (1) RWA pair discovery is **DB-cached, indexer-authoritative**

Source of truth: Equiteez `token` table with `in_allowlist=true` AND
their `orderbooks` with `in_allowlist=true`. The service:

- Runs a one-shot sync on startup (`SyncRWAPairs` in
  `internal/core/infrastructure/jobs/equiteez_rwa_sync.go`) that
  queries `GetAllowlistedTokensAndOrderbooks` and **upserts** every
  active (token_addr, orderbook_addr) into `rwa_pairs`.
- Pairs missing from the latest allowlist get `enabled=false`
  (soft-disable — preserves the synthetic `pair_id` for FK integrity in
  `rwa_quote_prices`).
- **Operator overrides survive sync**: if a pair was manually
  `enabled=false` (or in the future `true` after sync disabled it), the
  sync only updates metadata (`base_symbol`, `quote_symbol`,
  `last_synced_at`) and never re-flips `enabled`.
- The HTTP API and the price collector only ever read `rwa_pairs` —
  they never call Equiteez for pair listing on the hot path.

### (2) Price normalization uses the in-process tokens registry

`rwa_pairs.quote_symbol` (e.g. `USDT`) is looked up in the runtime
token registry (loaded from `tokens` at startup, see
[ADR-0010](0010-runtime-token-registry.md)). The registered
`TokenInfo.Decimals` (6 for USDT, 6 for USDC, etc.) drives a
`decimal.Decimal.Shift(-decimals)` on the raw indexer value:

```go
priceRaw  := decimal.NewFromFloat(56_250_000)   // micro-USDT
quoteDec  := 6                                  // from tokens.decimals
priceHuman := priceRaw.Shift(-int32(quoteDec))   // 56.25 USDT (exact)
```

**Pairs whose `quote_symbol` is not registered are skipped entirely**
with a `Warn`-level log (`rwa_unknown_quote_decimals_skipping_pair`) —
better an empty cell than a silently-corrupted price 10⁶× too large.

## Consequences

- ✅ Adding a new RWA pair is a config change in **Equiteez**, not in
  this repo. Restart picks it up via sync.
- ✅ Same path works for any quote currency the indexer adopts later
  (USDC, EURC, …) — operator only seeds the new symbol into
  `tokens` once, normalization works automatically.
- ✅ No special-cased `if quote == "USDT"` anywhere; behaviour scales
  by data, not code.
- ✅ Synthetic `pair_id` is stable across sync runs, so existing
  `rwa_quote_prices.pair_id` rows keep their references.
- ⚠️ Sync is one-shot at startup. New Equiteez allowlist additions
  during the day won't appear until next restart. Acceptable today;
  promote to periodic sync (cfg.RWA.SyncIntervalSeconds) when ops
  asks for it.
- ⚠️ A pair whose quote currency isn't yet in `tokens` is effectively
  **invisible to the collector** (skipped). The warning surfaces in
  logs and Grafana via `job_errors_total{reason="unknown_decimals"}`
  (TODO once we wire that label).
- ⚠️ Sync needs **reachable Equiteez at startup**. On indexer outage,
  `rwa_pairs` stays at last-known state (good); fresh deploys with
  empty `rwa_pairs` collect nothing until indexer recovers.
- 🔁 Re-open if (a) manual operator-override scenarios get more
  complex than "just toggle `enabled`" — then move to a separate
  `auto_enabled` column and an explicit `enabled = manual_override
  AND auto_enabled` rule; (b) Equiteez allowlist changes mid-day
  become frequent.

## Alternatives considered

- **Manual seed of `rwa_pairs` via migrations.** Was the fallback in
  Phase-1 of the rewrite. Doesn't scale, drifts. Rejected.
- **Drop `rwa_pairs` entirely; query Equiteez for the pair list on
  every collector tick.** Simpler in code, but breaks the FK from
  `rwa_quote_prices.pair_id` (no stable identifier) and forces the
  HTTP API to also call Equiteez — defeating the cache. Rejected.
- **Encode `decimals` per-orderbook in the GraphQL response.** The
  Equiteez schema does carry decimals on the token, but tying
  normalization to per-row metadata couples our writes to the indexer
  schema shape. The `tokens` registry centralizes the rule and lets
  operators override (e.g. if Equiteez ever returns wrong decimals
  for a misconfigured token).
- **Hardcoded `if quote == "USDT"` everywhere it's needed.**
  What we had. Doesn't survive USDC/EURC. Rejected.

## Notes

- Sync: `internal/core/infrastructure/jobs/equiteez_rwa_sync.go`,
  function `SyncRWAPairs`.
- Lookup repo: `LookupRepository.UpsertRWAPair` (insert-or-update on
  `(source_code, orderbook_addr)`),
  `LookupRepository.DisableMissingRWAPairs` (soft-disable).
- Collector: `internal/core/infrastructure/jobs/equiteez_rwa.go`,
  `orderbookToPoints(pair, ob, quoteDecimals, now)`.
- Tests: `equiteez_rwa_test.go` covers normalization (micro-USDT →
  USDT, satoshi-like 8-decimal, no-normalization fallback,
  zero/negative skip) and `lookupQuoteDecimals` (case-insensitive,
  unknown symbol).
- Operator runbook for the "X / EURC" case: insert into `tokens`
  with `decimals=6`, restart — sync picks up the pair, collector
  starts writing normalized prices.
