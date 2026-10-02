-- Include source_code in the latest-price index, matching the read predicates.
-- TimescaleDB does not support CREATE INDEX CONCURRENTLY on hypertables.
-- transaction_per_chunk holds the normal build lock on the current chunk only;
-- writes to other chunks can continue while this index is built.
--
-- Keep this file to ONE statement and do not wrap it in a transaction. The
-- integration runner sends each whole file through ExecContext, so adding a
-- second statement would create an implicit transaction and break the build.
-- 0022 repairs interrupted builds; 0024 retires the legacy index only after
-- checking that this replacement is valid.
CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
    ON token_prices (token_symbol, source_code, quote_currency, ts DESC)
    WITH (timescaledb.transaction_per_chunk);
