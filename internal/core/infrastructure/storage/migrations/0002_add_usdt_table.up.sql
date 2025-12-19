-- +migrate Up
-- Create usdt table in mev schema (similar to quotes table)

-- Create usdt table
CREATE TABLE IF NOT EXISTS mev.usdt (
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

-- Convert usdt to a hypertable on timestamp if TimescaleDB is present
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_extension
        WHERE extname = 'timescaledb'
    ) THEN
        PERFORM create_hypertable(
            'mev.usdt',
            'timestamp',
            if_not_exists => TRUE
        );
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; skipping hypertable creation';
    END IF;
END
$$ LANGUAGE plpgsql;

-- Create indexes for better performance
CREATE INDEX IF NOT EXISTS idx_mev_usdt_timestamp
    ON mev.usdt (timestamp);

CREATE INDEX IF NOT EXISTS idx_mev_usdt_timestamp_desc
    ON mev.usdt (timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_mev_usdt_deleted_at
    ON mev.usdt (deleted_at);
