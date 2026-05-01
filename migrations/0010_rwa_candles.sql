-- 0010_rwa_candles.sql
-- Stage 2 of charts.md — additional minute / day continuous aggregates for
-- RWA orderbook prices, plus compression policies on all three CAs.
--
-- The 1h CA already exists from 0007_rwa_quote_prices_aggregates.sql with
-- the column convention shared by FT prices (open_price, close_price,
-- min_price, max_price, samples). 0010 keeps `_1h` as-is — volume is parked
-- as Stage 4 (charts.md §1.1), so a `_1h` recreate would be churn for no
-- gain. When Stage 4 lands the volume migration recreates all three CAs.
--
-- See ADR-0015 for the full chart layout (3 CAs → 6 served intervals).
--
-- Continuous aggregates require timescaledb; gated by IF EXISTS check.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; skipping rwa_quote_prices candle aggregates';
        RETURN;
    END IF;

    -- 1m: hottest path; refresh policy keeps it within ~1 minute of live data.
    -- start_offset matches the live collector's 7-day query horizon (config.yaml).
    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'rwa_quote_prices_1m') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW rwa_quote_prices_1m
            WITH (timescaledb.continuous) AS
            SELECT
                pair_id,
                side,
                time_bucket('1 minute', ts)       AS bucket,
                avg(price)                         AS avg_price,
                min(price)                         AS min_price,
                max(price)                         AS max_price,
                first(price, ts)                   AS open_price,
                last(price, ts)                    AS close_price,
                count(*)                           AS samples
            FROM rwa_quote_prices
            GROUP BY pair_id, side, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('rwa_quote_prices_1m',
            start_offset      => INTERVAL '7 days',
            end_offset        => INTERVAL '1 minute',
            schedule_interval => INTERVAL '1 minute');
    END IF;

    -- 1d: deep-history path; refreshes hourly. start_offset is generous
    -- enough that backfill (`equiteez_backfill.go`) covers the same window
    -- on first sync after a deploy.
    IF NOT EXISTS (SELECT 1 FROM timescaledb_information.continuous_aggregates
                   WHERE view_name = 'rwa_quote_prices_1d') THEN
        EXECUTE $sql$
            CREATE MATERIALIZED VIEW rwa_quote_prices_1d
            WITH (timescaledb.continuous) AS
            SELECT
                pair_id,
                side,
                time_bucket('1 day', ts)          AS bucket,
                avg(price)                         AS avg_price,
                min(price)                         AS min_price,
                max(price)                         AS max_price,
                first(price, ts)                   AS open_price,
                last(price, ts)                    AS close_price,
                count(*)                           AS samples
            FROM rwa_quote_prices
            GROUP BY pair_id, side, bucket
            WITH NO DATA
        $sql$;

        PERFORM add_continuous_aggregate_policy('rwa_quote_prices_1d',
            start_offset      => INTERVAL '90 days',
            end_offset        => INTERVAL '1 hour',
            schedule_interval => INTERVAL '1 hour');
    END IF;

    -- Compression policies on all three RWA CAs. Same shape as 0008
    -- compresses the underlying hypertable; segmentby keys mirror the CA
    -- group_by so columnar layout is friendly to (pair, side) reads.
    BEGIN
        EXECUTE $sql$
            ALTER MATERIALIZED VIEW rwa_quote_prices_1m SET (
                timescaledb.compress,
                timescaledb.compress_segmentby = 'pair_id, side'
            )
        $sql$;
    EXCEPTION WHEN others THEN
        RAISE NOTICE 'compression already configured on rwa_quote_prices_1m';
    END;
    BEGIN
        PERFORM add_compression_policy('rwa_quote_prices_1m', INTERVAL '14 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on rwa_quote_prices_1m already exists';
    END;

    BEGIN
        EXECUTE $sql$
            ALTER MATERIALIZED VIEW rwa_quote_prices_1h SET (
                timescaledb.compress,
                timescaledb.compress_segmentby = 'pair_id, side'
            )
        $sql$;
    EXCEPTION WHEN others THEN
        RAISE NOTICE 'compression already configured on rwa_quote_prices_1h';
    END;
    BEGIN
        PERFORM add_compression_policy('rwa_quote_prices_1h', INTERVAL '30 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on rwa_quote_prices_1h already exists';
    END;

    BEGIN
        EXECUTE $sql$
            ALTER MATERIALIZED VIEW rwa_quote_prices_1d SET (
                timescaledb.compress,
                timescaledb.compress_segmentby = 'pair_id, side'
            )
        $sql$;
    EXCEPTION WHEN others THEN
        RAISE NOTICE 'compression already configured on rwa_quote_prices_1d';
    END;
    BEGIN
        PERFORM add_compression_policy('rwa_quote_prices_1d', INTERVAL '180 days');
    EXCEPTION WHEN duplicate_object THEN
        RAISE NOTICE 'compression policy on rwa_quote_prices_1d already exists';
    END;
END $$ LANGUAGE plpgsql;
