-- 0004_rwa_quote_prices.sql
-- Long-format hypertable for RWA orderbook prices. metric ('side') is bid/ask/last/mid
-- and `size` carries optional volume — only RWA needs it, FT-quotes leave that column
-- absent in the FT table.

CREATE TABLE IF NOT EXISTS rwa_quote_prices (
    pair_id  bigint         NOT NULL REFERENCES rwa_pairs(id),
    ts       timestamptz    NOT NULL,
    side     text           NOT NULL CHECK (side IN ('bid','ask','last','mid')),
    price    numeric(38,18) NOT NULL,
    size     numeric(38,18),
    PRIMARY KEY (pair_id, side, ts)
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'rwa_quote_prices',
            'ts',
            chunk_time_interval => INTERVAL '1 day',
            if_not_exists       => TRUE
        );
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; rwa_quote_prices stays a regular table';
    END IF;
END $$ LANGUAGE plpgsql;

CREATE INDEX IF NOT EXISTS idx_rwa_quote_prices_latest
    ON rwa_quote_prices (pair_id, side, ts DESC);
