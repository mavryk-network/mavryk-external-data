-- Migration: 001_init.sql
-- Hypertable for all tokens (Timescale when available), idempotent re-runs.

DO $$
BEGIN
    BEGIN
        EXECUTE 'CREATE EXTENSION IF NOT EXISTS timescaledb';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'TimescaleDB not available, skipping extension creation';
    END;
END $$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS quotes (
    token      text          NOT NULL,
    timestamp  timestamptz   NOT NULL,
    btc        numeric(20,8) DEFAULT 0,
    usd        numeric(20,8) DEFAULT 0,
    eur        numeric(20,8) DEFAULT 0,
    cny        numeric(20,8) DEFAULT 0,
    jpy        numeric(20,8) DEFAULT 0,
    krw        numeric(20,8) DEFAULT 0,
    eth        numeric(20,8) DEFAULT 0,
    gbp        numeric(20,8) DEFAULT 0,
    created_at timestamptz   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (token, timestamp)
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'quotes',
            'timestamp',
            chunk_time_interval => INTERVAL '7 days',
            if_not_exists       => TRUE
        );
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; skipping hypertable creation';
    END IF;
END $$ LANGUAGE plpgsql;

-- Hot-path read index: per-token lookup and latest-first scans.
CREATE INDEX IF NOT EXISTS idx_quotes_token_timestamp_desc
    ON quotes (token, timestamp DESC);
