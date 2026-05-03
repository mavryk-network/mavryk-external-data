-- 0011_token_prices_minute.sql
-- Minute-level continuous aggregate for FT prices: OHLC per (token, source, currency).
-- Mirrors the `_1h` and `_1d` views from 0006; chosen so the FA chart
-- repository can serve 1m / 5m / 15m via this single CA (5m / 15m via
-- repository-side re-bucket).
--
-- See ADR-0015 for the full rationale.
--
-- At the current 60s polling cadence the bucket holds 1 sample on average
-- — open=high=low=close. That's degenerate but valid; raising the cadence
-- (or switching to /coins/{id}/market_chart/range when volume ingestion
-- lands) starts producing real intra-bucket dynamics without any schema
-- change.
--
-- Continuous aggregates require timescaledb; gated by IF EXISTS check.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping token_prices_1m';
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'token_prices_1m') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW token_prices_1m
            WITH (timescaledb.continuous) AS
            SELECT
                token_symbol,
                source_code,
                quote_currency,
                time_bucket('1 minute', ts)       AS bucket,
                avg(price)                         AS avg_price,
                min(price)                         AS min_price,
                max(price)                         AS max_price,
                first(price, ts)                   AS open_price,
                last(price, ts)                    AS close_price,
                count(*)                           AS samples
            FROM token_prices
            GROUP BY token_symbol, source_code, quote_currency, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('token_prices_1m',
            start_offset      => INTERVAL '7 days',
            end_offset        => INTERVAL '1 minute',
            schedule_interval => INTERVAL '1 minute');
    END IF;

    -- Compression: same shape as the underlying hypertable (0008). Older
    -- minute rows compress aggressively since segmentby keys are wide.
    BEGIN
        EXECUTE $sql$
            ALTER MATERIALIZED VIEW token_prices_1m SET (
                timescaledb.compress,
                timescaledb.compress_segmentby = 'token_symbol, source_code, quote_currency'
            )
        $sql$;
    EXCEPTION WHEN others THEN
        RAISE NOTICE 'compression already configured on token_prices_1m';
    END;
    BEGIN
        PERFORM add_compression_policy('token_prices_1m', INTERVAL '14 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on token_prices_1m already exists';
    END;
END $$ LANGUAGE plpgsql;
