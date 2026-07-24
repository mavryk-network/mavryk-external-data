-- 0014_compression_retention_tickers.sql
-- Compression + retention for token_tickers.
--
-- Compress after 7d, retain raw 90d. Shorter than token_prices (14d / 2y)
-- because (a) tickers churn faster — every 5min × N exchange-target pairs,
-- and (b) we have no validated retro-analytics use beyond 90 days. Adjust
-- when a real consumer asks for longer-window joins.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping compression/retention on token_tickers';
        RETURN;
    END IF;

    -- Guard the ALTER: re-running `SET (timescaledb.compress, ...)` once a chunk
    -- is compressed errors with "cannot change configuration on already
    -- compressed chunks". Skip when compression is already configured (settings
    -- are fixed at first run). See 0008 for the same pattern.
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.compression_settings
        WHERE hypertable_name = 'token_tickers'
    ) THEN
        EXECUTE $sql$
            ALTER TABLE token_tickers SET (
                timescaledb.compress,
                timescaledb.compress_segmentby = 'token_symbol, source_code, exchange_id, target_symbol',
                timescaledb.compress_orderby   = 'ts DESC'
            )
        $sql$;
    END IF;
    BEGIN
        PERFORM add_compression_policy('token_tickers', INTERVAL '7 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on token_tickers already exists';
    END;
    BEGIN
        PERFORM add_retention_policy('token_tickers', INTERVAL '90 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'retention policy on token_tickers already exists';
    END;
END $$ LANGUAGE plpgsql;
