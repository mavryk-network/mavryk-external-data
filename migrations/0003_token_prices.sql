-- 0003_token_prices.sql
-- Long-format hypertable for FT-quotes (CoinGecko and any future CEX/DEX-aggregator).
-- One row = one (token, source, ts, quote_currency, price). Adding a new currency or
-- source is a runtime concern — no schema migration needed.

CREATE TABLE IF NOT EXISTS token_prices (
    token_symbol   text           NOT NULL REFERENCES tokens(symbol),
    source_code    text           NOT NULL REFERENCES sources(code),
    ts             timestamptz    NOT NULL,
    quote_currency text           NOT NULL,
    price          numeric(38,18) NOT NULL,
    PRIMARY KEY (token_symbol, source_code, quote_currency, ts)
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'token_prices',
            'ts',
            chunk_time_interval => INTERVAL '7 days',
            if_not_exists       => TRUE
        );
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; token_prices stays a regular table';
    END IF;
END $$ LANGUAGE plpgsql;

-- The latest-price index is upgraded by 0022-0024. These files are replayed
-- on every deploy, including populated databases: leave the legacy index in
-- place until its replacement has finished building one chunk at a time.
-- Do not create either index here, or this earlier blocking build would run
-- before the staged upgrade (and recreate the retired index on every replay).

-- Range scans by source (rare, but cheap to maintain on a hypertable).
CREATE INDEX IF NOT EXISTS idx_token_prices_source_ts
    ON token_prices (source_code, ts DESC);
