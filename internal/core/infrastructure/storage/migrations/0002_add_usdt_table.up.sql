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
DECLARE
    table_is_empty BOOLEAN;
    is_hypertable BOOLEAN;
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        -- Check if already a hypertable
        SELECT EXISTS (
            SELECT 1 FROM timescaledb_information.hypertables 
            WHERE hypertable_schema = 'mev' AND hypertable_name = 'usdt'
        ) INTO is_hypertable;
        
        -- Only proceed if not already a hypertable
        IF NOT is_hypertable THEN
            -- Check if table is empty (table must exist at this point due to CREATE TABLE IF NOT EXISTS above)
            SELECT NOT EXISTS (SELECT 1 FROM mev.usdt LIMIT 1) INTO table_is_empty;
            
            IF table_is_empty THEN
                -- Table is empty, safe to create hypertable
                BEGIN
                    PERFORM create_hypertable('mev.usdt', 'timestamp', if_not_exists => TRUE);
                    RAISE NOTICE 'Converted mev.usdt to hypertable';
                EXCEPTION WHEN OTHERS THEN
                    RAISE NOTICE 'Skipping hypertable creation: %', SQLERRM;
                END;
            ELSE
                -- Table is not empty, use migrate_data option to convert existing data
                BEGIN
                    PERFORM create_hypertable('mev.usdt', 'timestamp', if_not_exists => TRUE, migrate_data => TRUE);
                    RAISE NOTICE 'Converted mev.usdt to hypertable with data migration';
                EXCEPTION WHEN OTHERS THEN
                    -- If migrate_data also fails, just skip hypertable creation
                    RAISE NOTICE 'Skipping hypertable creation (table not empty): %', SQLERRM;
                END;
            END IF;
        END IF;
    END IF;
END $$ LANGUAGE plpgsql;

-- Create indexes for better performance
CREATE INDEX IF NOT EXISTS idx_mev_usdt_timestamp
    ON mev.usdt (timestamp);

CREATE INDEX IF NOT EXISTS idx_mev_usdt_timestamp_desc
    ON mev.usdt (timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_mev_usdt_deleted_at
    ON mev.usdt (deleted_at);
