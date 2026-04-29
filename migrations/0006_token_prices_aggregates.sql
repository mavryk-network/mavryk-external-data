-- 0006_token_prices_aggregates.sql
-- Continuous aggregates for FT prices: 1h and 1d OHLC + min/max/avg per
-- (token, source, currency). Dashboards and historical APIs read from these instead
-- of scanning raw rows.
--
-- Continuous aggregates require timescaledb; gated by IF EXISTS check.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping token_prices continuous aggregates';
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'token_prices_1h') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW token_prices_1h
            WITH (timescaledb.continuous) AS
            SELECT
                token_symbol,
                source_code,
                quote_currency,
                time_bucket('1 hour', ts)        AS bucket,
                avg(price)                        AS avg_price,
                min(price)                        AS min_price,
                max(price)                        AS max_price,
                first(price, ts)                  AS open_price,
                last(price, ts)                   AS close_price,
                count(*)                          AS samples
            FROM token_prices
            GROUP BY token_symbol, source_code, quote_currency, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('token_prices_1h',
            start_offset      => INTERVAL '7 days',
            end_offset        => INTERVAL '1 hour',
            schedule_interval => INTERVAL '15 minutes');
    END IF;

    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'token_prices_1d') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW token_prices_1d
            WITH (timescaledb.continuous) AS
            SELECT
                token_symbol,
                source_code,
                quote_currency,
                time_bucket('1 day', ts)         AS bucket,
                avg(price)                        AS avg_price,
                min(price)                        AS min_price,
                max(price)                        AS max_price,
                first(price, ts)                  AS open_price,
                last(price, ts)                   AS close_price,
                count(*)                          AS samples
            FROM token_prices
            GROUP BY token_symbol, source_code, quote_currency, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('token_prices_1d',
            start_offset      => INTERVAL '30 days',
            end_offset        => INTERVAL '1 day',
            schedule_interval => INTERVAL '1 hour');
    END IF;
END $$ LANGUAGE plpgsql;
