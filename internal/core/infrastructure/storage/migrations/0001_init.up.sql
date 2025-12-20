-- +migrate Up
-- Create mev schema
CREATE SCHEMA IF NOT EXISTS mev;

-- Try to enable TimescaleDB extension if available; ignore if not installed
DO $$
BEGIN
    BEGIN
        EXECUTE 'CREATE EXTENSION IF NOT EXISTS timescaledb';
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'TimescaleDB not available, skipping extension creation';
    END;
END $$ LANGUAGE plpgsql;

-- Create quotes table in mev schema
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

-- Convert quotes to a hypertable on timestamp if TimescaleDB is present
-- Skip if table mvrk already exists (means migration 0003 was already applied manually)
DO $$
DECLARE
    quotes_exists BOOLEAN;
    mvrk_exists BOOLEAN;
    table_is_empty BOOLEAN;
    is_hypertable BOOLEAN;
BEGIN
    -- Check if mvrk table already exists (migration 0003 already applied)
    SELECT EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_schema = 'mev' AND table_name = 'mvrk'
    ) INTO mvrk_exists;
    
    -- If mvrk exists, skip this migration's hypertable conversion
    IF mvrk_exists THEN
        RAISE NOTICE 'Table mev.mvrk already exists, skipping quotes hypertable conversion';
        RETURN;
    END IF;
    
    -- Check if quotes table exists
    SELECT EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_schema = 'mev' AND table_name = 'quotes'
    ) INTO quotes_exists;
    
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') AND quotes_exists THEN
        -- Check if already a hypertable
        SELECT EXISTS (
            SELECT 1 FROM timescaledb_information.hypertables 
            WHERE hypertable_schema = 'mev' AND hypertable_name = 'quotes'
        ) INTO is_hypertable;
        
        -- Only proceed if not already a hypertable
        IF NOT is_hypertable THEN
            -- Check if table is empty
            SELECT NOT EXISTS (SELECT 1 FROM mev.quotes LIMIT 1) INTO table_is_empty;
            
            IF table_is_empty THEN
                -- Table is empty, safe to create hypertable
                BEGIN
                    PERFORM create_hypertable('mev.quotes', 'timestamp', if_not_exists => TRUE);
                    RAISE NOTICE 'Converted mev.quotes to hypertable';
                EXCEPTION WHEN OTHERS THEN
                    RAISE NOTICE 'Skipping hypertable creation: %', SQLERRM;
                END;
            ELSE
                -- Table is not empty - skip hypertable creation to avoid errors
                -- Existing tables with data should be converted manually if needed
                RAISE NOTICE 'Table mev.quotes is not empty, skipping hypertable creation. Convert manually if needed.';
            END IF;
        END IF;
    END IF;
END $$ LANGUAGE plpgsql;

-- Create indexes for better performance (only if quotes table exists)
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_schema = 'mev' AND table_name = 'quotes'
    ) THEN
        CREATE INDEX IF NOT EXISTS idx_mev_quotes_timestamp ON mev.quotes(timestamp);
        CREATE INDEX IF NOT EXISTS idx_mev_quotes_timestamp_desc ON mev.quotes(timestamp DESC);
        CREATE INDEX IF NOT EXISTS idx_mev_quotes_deleted_at ON mev.quotes(deleted_at);
    END IF;
END $$ LANGUAGE plpgsql;
