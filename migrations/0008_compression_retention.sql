-- 0008_compression_retention.sql
-- Compression + retention policies for hot-path hypertables.
-- Dropped via DROP POLICY ... commands if you ever need to disable.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping compression/retention policies';
        RETURN;
    END IF;

    -- token_prices: compress chunks older than 14 days.
    EXECUTE $sql$
        ALTER TABLE token_prices SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'token_symbol, quote_currency, source_code',
            timescaledb.compress_orderby   = 'ts DESC'
        )
    $sql$;
    BEGIN
        PERFORM add_compression_policy('token_prices', INTERVAL '14 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on token_prices already exists';
    END;
    BEGIN
        PERFORM add_retention_policy('token_prices', INTERVAL '2 years');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'retention policy on token_prices already exists';
    END;

    -- rwa_quote_prices: compress chunks older than 7 days.
    EXECUTE $sql$
        ALTER TABLE rwa_quote_prices SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'pair_id, side',
            timescaledb.compress_orderby   = 'ts DESC'
        )
    $sql$;
    BEGIN
        PERFORM add_compression_policy('rwa_quote_prices', INTERVAL '7 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on rwa_quote_prices already exists';
    END;
    BEGIN
        PERFORM add_retention_policy('rwa_quote_prices', INTERVAL '2 years');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'retention policy on rwa_quote_prices already exists';
    END;
END $$ LANGUAGE plpgsql;
