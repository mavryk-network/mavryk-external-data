-- Complete latest observations, separate from historical positive-only ticks.
-- Nullable sides must overwrite previous values when a book becomes empty.
CREATE TABLE IF NOT EXISTS rwa_market_snapshots (
    pair_id bigint PRIMARY KEY REFERENCES rwa_pairs(id),
    network text NOT NULL,
    indexer_generation text NOT NULL,
    observed_at timestamptz NOT NULL,
    token_addr text NOT NULL,
    token_id integer,
    quote_addr text NOT NULL,
    quote_token_id integer,
    orderbook_addr text NOT NULL,
    native_quote text NOT NULL,
    reason text NOT NULL DEFAULT '',
    bid numeric(38,18),
    ask numeric(38,18),
    bid_expires_at timestamptz,
    ask_expires_at timestamptz,
    last_price numeric(38,18),
    last_trade_at timestamptz,
    CHECK (bid IS NULL OR bid > 0),
    CHECK (ask IS NULL OR ask > 0),
    CHECK (last_price IS NULL OR last_price > 0)
);

-- NULL until a fresh sync verifies a single, supported payment identity.
-- Existing launch prices/endpoints keep their original behavior.
ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS pricing_quote_token_id integer;
