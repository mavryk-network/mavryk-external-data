-- 0002_lookup_tables.sql
-- Lookup-tables backing the long-format price model:
--   sources  — registry of upstream providers (CoinGecko, Equiteez, future DEX/oracle).
--   tokens   — fungible tokens (FT) the live/backfill jobs collect from CoinGecko.
--   rwa_pairs — RWA orderbook pairs, populated by the Equiteez allowlist sync
--               (jobs/equiteez_rwa_sync.go). One row = one (source, orderbook
--               contract). A single token may own multiple orderbooks (different
--               quote currencies / regulatory tranches), so the natural key is
--               (source_code, orderbook_addr) — NOT the token contract.
-- All hot-path tables FK into these, so an INSERT with an unknown source/token
-- is impossible.

CREATE TABLE IF NOT EXISTS sources (
    code       text PRIMARY KEY,
    name       text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('cex','dex','indexer','oracle')),
    created_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tokens (
    symbol     text PRIMARY KEY,
    name       text NOT NULL,
    decimals   smallint NOT NULL DEFAULT 0,
    cg_id      text,
    enabled    boolean NOT NULL DEFAULT TRUE,
    created_at timestamptz NOT NULL DEFAULT NOW()
);

-- rwa_pairs columns:
--   * token_addr             — owning token contract (Tezos address; populated by sync).
--   * orderbook_addr         — orderbook contract; what the collector actually polls.
--   * equiteez_orderbook_id  — indexer's internal integer id for the orderbook
--                              (Hasura `orderbook.id`). Cached so the backfill
--                              job (jobs/equiteez_backfill.go) can join against
--                              orderbook_order without resolving the Tezos
--                              address every batch. Populated by SyncRWAPairs;
--                              NULL until the next sync run.
--   * base_symbol            — human label for the base asset (derived during sync).
--   * quote_symbol           — quote currency label (e.g. "USDT").
--   * enabled                — local override; the sync respects it (won't
--                              re-enable an operator-disabled pair) but will
--                              set it to false when a previously-allowlisted
--                              orderbook disappears.
--   * last_synced_at         — audit trail for the discovery sync.
CREATE TABLE IF NOT EXISTS rwa_pairs (
    id                     bigserial PRIMARY KEY,
    base_symbol            text NOT NULL,
    quote_symbol           text NOT NULL,
    source_code            text NOT NULL REFERENCES sources(code),
    token_addr             text,
    orderbook_addr         text,
    equiteez_orderbook_id  integer,
    enabled                boolean NOT NULL DEFAULT TRUE,
    last_synced_at         timestamptz,
    created_at             timestamptz NOT NULL DEFAULT NOW()
);

-- Natural key: orderbook contracts are unique within a source.
CREATE UNIQUE INDEX IF NOT EXISTS rwa_pairs_source_orderbook_uq
    ON rwa_pairs (source_code, orderbook_addr)
    WHERE orderbook_addr IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_rwa_pairs_enabled
    ON rwa_pairs (source_code, enabled);

-- Backfill lookup: pair_id → equiteez_orderbook_id is hot during each
-- backfill tick; partial index keeps it small (only allowlisted pairs).
CREATE INDEX IF NOT EXISTS idx_rwa_pairs_equiteez_orderbook_id
    ON rwa_pairs (equiteez_orderbook_id)
    WHERE equiteez_orderbook_id IS NOT NULL;
