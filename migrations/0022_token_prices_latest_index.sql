-- 0022_token_prices_latest_index.sql
-- Align the latest-point index with the query that uses it.
--
-- Why: latestPerMetric filters WHERE token_symbol = ? AND source_code = ?
-- then DISTINCT ON (quote_currency) ... ORDER BY quote_currency, ts DESC.
-- The old idx_token_prices_latest (token_symbol, quote_currency, ts DESC)
-- omitted source_code, so on plain Postgres the filter degraded toward a
-- per-token history scan. Include source_code in the key so the read is an
-- index-ordered walk per (token, source, currency).

-- 0003 now creates the new index for fresh databases; this file transitions
-- existing ones (create-if-missing, then drop the superseded index).
CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
    ON token_prices (token_symbol, source_code, quote_currency, ts DESC);

DROP INDEX IF EXISTS idx_token_prices_latest;
