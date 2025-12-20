-- Create mev schema
CREATE SCHEMA IF NOT EXISTS mev;

DO $$
BEGIN
    BEGIN
        EXECUTE 'CREATE EXTENSION IF NOT EXISTS timescaledb';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'TimescaleDB not available, skipping extension creation';
    END;
END $$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS mev.quotes (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP WITH TIME ZONE NOT NULL,
    btc DECIMAL(20,8) DEFAULT 0,
    usd DECIMAL(20,8) DEFAULT 0,
    eur DECIMAL(20,8) DEFAULT 0,
    cny DECIMAL(20,8) DEFAULT 0,
    jpy DECIMAL(20,8) DEFAULT 0,
    krw DECIMAL(20,8) DEFAULT 0,
    eth DECIMAL(20,8) DEFAULT 0,
    gbp DECIMAL(20,8) DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_extension WHERE extname = 'timescaledb'
    ) THEN
        PERFORM create_hypertable('mev.quotes', 'timestamp', if_not_exists => TRUE);
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; skipping hypertable creation';
    END IF;
END $$ LANGUAGE plpgsql;

CREATE INDEX IF NOT EXISTS idx_mev_quotes_timestamp ON mev.quotes(timestamp);
CREATE INDEX IF NOT EXISTS idx_mev_quotes_timestamp_desc ON mev.quotes(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_mev_quotes_deleted_at ON mev.quotes(deleted_at);

