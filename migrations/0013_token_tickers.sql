-- 0013_token_tickers.sql
-- Long-format hypertable for per-exchange ticker data (CoinGecko /coins/{id}/tickers).
-- One row = one observation at (token, source, exchange, target_symbol, ts).
--
-- Native units only: `last_price` is in `target_symbol` units (e.g. for MVRK/BTC
-- it's BTC per MVRK), `volume_24h_base` is in the *token* (e.g. count of MVRK).
-- Multi-currency rendering (?in=usd,eur,...) happens read-side via the
-- PriceConverter (ADR-0013). No stored converted_*_usd columns.
--
-- Stale-ness is derived at read time from `ts < now - server.ticker_stale_after`
-- (default 1h) — not stored.
--
-- The (token, exchange, target, ts DESC) index is the hot path for
--   * /v1/tickers/:token/latest    — DISTINCT ON (exchange,target) ORDER BY ts DESC
--   * 1D change LATERAL join       — find freshest row with ts <= now - 24h
-- Both are O(log n) seeks on a per-pair leaf.

CREATE TABLE IF NOT EXISTS token_tickers (
    token_symbol         text           NOT NULL REFERENCES tokens(symbol),
    source_code          text           NOT NULL REFERENCES sources(code),
    exchange_id          text           NOT NULL REFERENCES exchanges(id),
    target_symbol        text           NOT NULL,
    ts                   timestamptz    NOT NULL,
    last_price           numeric(38,18) NOT NULL,
    volume_24h_base      numeric(38,18),
    bid_ask_spread_pct   numeric(20,10),
    trust_score          text,
    is_anomaly           boolean        NOT NULL DEFAULT FALSE,
    trade_url            text,
    last_traded_at       timestamptz,
    PRIMARY KEY (token_symbol, source_code, exchange_id, target_symbol, ts)
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable(
            'token_tickers',
            'ts',
            chunk_time_interval => INTERVAL '7 days',
            if_not_exists       => TRUE
        );
    ELSE
        RAISE NOTICE 'TimescaleDB not installed; token_tickers stays a regular table';
    END IF;
END $$ LANGUAGE plpgsql;

-- Latest per (exchange, target) is the dominant read pattern.
CREATE INDEX IF NOT EXISTS idx_token_tickers_latest
    ON token_tickers (token_symbol, exchange_id, target_symbol, ts DESC);

-- Token-wide scans (window queries via TODOS-3 once history endpoint lands).
CREATE INDEX IF NOT EXISTS idx_token_tickers_token_ts
    ON token_tickers (token_symbol, ts DESC);
