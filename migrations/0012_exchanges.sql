-- 0012_exchanges.sql
-- Lookup table for crypto exchanges that report MVRK (and future tokens)
-- tickers. Populated by the CoinGecko tickers job — UPSERT semantics keep
-- name/logo/kind fresh as CG amends them.
--
-- `id` matches CoinGecko's market.identifier (e.g. "binance", "kraken").
-- `kind` is derived in code (domain/tickers/exchange_kind.go) since CG /tickers
-- does not tag CEX vs DEX. Default 'cex' is the safe fallback.
--
-- FK target for token_tickers.exchange_id. Light row count (low hundreds at
-- most), so plain B-tree on the PK is enough — no Timescale needed here.

CREATE TABLE IF NOT EXISTS exchanges (
    id                    text PRIMARY KEY,
    name                  text NOT NULL,
    logo_url              text,
    kind                  text NOT NULL DEFAULT 'cex'
                          CHECK (kind IN ('cex','dex')),
    has_trading_incentive boolean NOT NULL DEFAULT FALSE,
    last_seen_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_exchanges_last_seen
    ON exchanges (last_seen_at DESC);
