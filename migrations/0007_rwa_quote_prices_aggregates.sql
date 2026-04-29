-- 0007_rwa_quote_prices_aggregates.sql
-- Continuous aggregates for RWA orderbook: 1h OHLC per (pair, side).

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping rwa_quote_prices continuous aggregates';
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'rwa_quote_prices_1h') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW rwa_quote_prices_1h
            WITH (timescaledb.continuous) AS
            SELECT
                pair_id,
                side,
                time_bucket('1 hour', ts)        AS bucket,
                avg(price)                        AS avg_price,
                min(price)                        AS min_price,
                max(price)                        AS max_price,
                first(price, ts)                  AS open_price,
                last(price, ts)                   AS close_price,
                count(*)                          AS samples
            FROM rwa_quote_prices
            GROUP BY pair_id, side, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('rwa_quote_prices_1h',
            start_offset      => INTERVAL '3 days',
            end_offset        => INTERVAL '1 hour',
            schedule_interval => INTERVAL '15 minutes');
    END IF;
END $$ LANGUAGE plpgsql;
