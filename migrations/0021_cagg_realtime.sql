-- 0021_cagg_realtime.sql
-- Pin real-time aggregation ON for every continuous aggregate.
--
-- Why: the CAGGs were created without an explicit materialized_only setting.
-- TimescaleDB 2.13 flipped the CREATE default to true, so on the pinned 2.26
-- image every chart read (QueryCandles never scans raw tables) silently missed
-- the newest in-progress bucket — up to a full day on the 1d views, hiding an
-- intraday ATH from AllTimeHighLast until the daily bucket materialized.
-- materialized_only=false unions the materialized data with a real-time scan
-- of the raw hypertable's tail, restoring the intended freshness.
--
-- Idempotent: re-applying the same setting is a no-op ALTER.

DO $$
DECLARE
    v TEXT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping materialized_only pinning';
        RETURN;
    END IF;

    FOR v IN
        SELECT view_name FROM timescaledb_information.continuous_aggregates
        WHERE view_name IN (
            'token_prices_1m', 'token_prices_1h', 'token_prices_1d',
            'rwa_quote_prices_1m', 'rwa_quote_prices_1h', 'rwa_quote_prices_1d'
        )
    LOOP
        EXECUTE format('ALTER MATERIALIZED VIEW %I SET (timescaledb.materialized_only = false)', v);
    END LOOP;
END $$;
