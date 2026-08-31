-- 0022_token_prices_latest_index.sql
-- Align the latest-point index with the query that uses it.
--
-- Why: latestPerMetric filters WHERE token_symbol = ? AND source_code = ?
-- then DISTINCT ON (quote_currency) ... ORDER BY quote_currency, ts DESC.
-- The old idx_token_prices_latest (token_symbol, quote_currency, ts DESC)
-- omitted source_code, so on plain Postgres the filter degraded toward a
-- per-token history scan. Include source_code in the key so the read is an
-- index-ordered walk per (token, source, currency).
--
-- This is the ONLY place the index is created — 0003 deliberately does not,
-- because the runners re-apply every file on every deploy (there is no
-- migration-tracking table), so an earlier plain CREATE INDEX would win the
-- race and do the blocking build before this one could run.
--
-- The superseded index is dropped in 0023, NOT here: transaction_per_chunk
-- below refuses to run inside a transaction block, and a second statement in
-- this file would put it in one (the integration harness applies each file in a
-- single ExecContext, which Postgres treats as an implicit transaction once it
-- holds more than one statement). One statement per file keeps the non-blocking
-- build available to both runners.
--
-- transaction_per_chunk commits the build one chunk at a time instead of
-- holding a SHARE lock on the whole hypertable for its duration, so ingestion
-- keeps running while the migration does. Note that CREATE INDEX CONCURRENTLY
-- is not an option here — TimescaleDB rejects it on hypertables.
CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
    ON token_prices (token_symbol, source_code, quote_currency, ts DESC)
    WITH (timescaledb.transaction_per_chunk);
