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

-- Hot path: latest price per (token, source, currency). The DROP retires the
-- old narrower index on databases that built it before source_code joined the
-- key (its absence degraded latest reads to per-token scans).
DROP INDEX IF EXISTS idx_token_prices_latest;
CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
    ON token_prices (token_symbol, source_code, quote_currency, ts DESC);

-- Range scans by source (rare, but cheap to maintain on a hypertable).
CREATE INDEX IF NOT EXISTS idx_token_prices_source_ts
    ON token_prices (source_code, ts DESC);
