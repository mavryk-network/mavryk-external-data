-- 0021_cagg_realtime.sql
-- Pin real-time aggregation ON for every CAGG: TimescaleDB 2.13 flipped the
-- CREATE default to materialized_only=true, hiding the newest in-progress
-- bucket from every chart read (QueryCandles never scans raw tables).
-- The AND materialized_only filter keeps replays from re-taking the
-- ACCESS EXCLUSIVE lock on every deploy.

DO $$
DECLARE
    v TEXT;
BEGIN
    FOR v IN
        SELECT view_name FROM timescaledb_information.continuous_aggregates
        WHERE view_name IN (
            'token_prices_1m', 'token_prices_1h', 'token_prices_1d',
            'rwa_quote_prices_1m', 'rwa_quote_prices_1h', 'rwa_quote_prices_1d'
        )
        AND materialized_only
    LOOP
        EXECUTE format('ALTER MATERIALIZED VIEW %I SET (timescaledb.materialized_only = false)', v);
    END LOOP;
END $$;
