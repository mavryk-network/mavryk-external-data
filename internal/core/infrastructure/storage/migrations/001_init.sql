-- Create mvkt schema
CREATE SCHEMA IF NOT EXISTS mvkt;

-- Create quotes table in mvkt schema
CREATE TABLE IF NOT EXISTS mvkt.quotes (
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

-- Create indexes for better performance
CREATE INDEX IF NOT EXISTS idx_mvkt_quotes_timestamp ON mvkt.quotes(timestamp);
CREATE INDEX IF NOT EXISTS idx_mvkt_quotes_timestamp_desc ON mvkt.quotes(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_mvkt_quotes_deleted_at ON mvkt.quotes(deleted_at);

-- Create GIN index for full-text search if needed
-- CREATE INDEX IF NOT EXISTS idx_mvkt_quotes_gin ON mvkt.quotes USING gin(to_tsvector('english', timestamp::text));

-- Grant permissions (adjust as needed for your environment)
-- GRANT USAGE ON SCHEMA mvkt TO your_app_user;
-- GRANT SELECT, INSERT, UPDATE, DELETE ON mvkt.quotes TO your_app_user;
