-- 0017_rwa_pairs_quote_addr.sql
-- On-chain address of the pair's quote token.
--
-- Why: consumers that build transactions (escrow approvals, balance checks)
-- need the quote token's contract address, not just its symbol. The discovery
-- sync already receives it from the indexer (`orderbook.currencies[0].token`)
-- but never persisted it, so /v1/pairs/rwa could not serve it.
--
-- Migration semantics: existing rows get quote_addr = NULL; the next discovery
-- tick (hourly) fills it in for every allowlisted pair. Nullable by design —
-- a degraded indexer response without currency rows must not block the upsert.

ALTER TABLE rwa_pairs ADD COLUMN IF NOT EXISTS quote_addr TEXT;

COMMENT ON COLUMN rwa_pairs.quote_addr IS
    'On-chain contract address of the quote token (e.g. USDT KT1...). Populated by the discovery sync; NULL until the first sync after this migration.';
