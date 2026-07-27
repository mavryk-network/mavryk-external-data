-- 0016_rwa_launches.sql
-- Primary-issuance (launchpad) state per RWA token.
--
-- Why a separate table from rwa_pairs: rwa_pairs models a *secondary market*
-- pair (token × orderbook contract). A token in primary issuance has no
-- orderbook yet — XAUG / MCDX / KHBE are allowlisted with an active launch but
-- zero orderbooks — so it produces no rwa_pairs row at all and would be
-- invisible to GET /v1/rwa. Keeping issuance in its own table avoids
-- overloading rwa_pairs with nullable orderbook columns.
--
-- One row per (source, token_addr): a token can have several launchpad_launch
-- rows (history, re-issuance), but only the surfaced one is stored — selection
-- happens in the sync job (newest by updated_at, active preferred).
--
-- Numeric choices:
--   * total_bought / max_amount_cap are raw on-chain nat values. NUMERIC(78,0)
--     rather than BIGINT because supply-scale amounts of an 18-decimal token
--     overflow int64 (2.5e12 fits today, 1e24 would not).
--   * price is the human (decimals-applied) base-tier price per token in the
--     payment currency — value-bounded, so NUMERIC(38,18) matches the precision
--     used by token_prices / rwa_quote_prices.
--   * progress_percent is stored, not derived on read, so the API never has to
--     re-divide raw nats; DOUBLE PRECISION holds values as small as 2.7e-9.

CREATE TABLE IF NOT EXISTS rwa_launches (
    source_code      TEXT             NOT NULL,
    token_addr       TEXT             NOT NULL,
    token_id         INTEGER          NOT NULL DEFAULT 0,

    launch_id        INTEGER          NOT NULL,
    name             TEXT             NOT NULL DEFAULT '',
    -- active | inactive | paused | closed (mirrors the launchpad status enum)
    status           TEXT             NOT NULL DEFAULT '',
    -- "purchasable right now": status/pause flags plus the sale window.
    active           BOOLEAN          NOT NULL DEFAULT FALSE,

    base_symbol      TEXT             NOT NULL DEFAULT '',
    quote_symbol     TEXT             NOT NULL DEFAULT '',
    price            NUMERIC(38,18),
    total_bought     NUMERIC(78,0),
    max_amount_cap   NUMERIC(78,0),
    progress_percent DOUBLE PRECISION NOT NULL DEFAULT 0,

    sale_start       TIMESTAMPTZ,
    sale_end         TIMESTAMPTZ,
    sale_closed      TIMESTAMPTZ,

    -- Operator kill-switch, mirroring rwa_pairs.enabled. The sync job never
    -- writes this column on an existing row (see LaunchRepository.Upsert).
    enabled          BOOLEAN          NOT NULL DEFAULT TRUE,
    last_synced_at   TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ      NOT NULL DEFAULT NOW(),

    PRIMARY KEY (source_code, token_addr)
);

-- Read path: GET /v1/rwa lists enabled rows for one source in a stable order.
CREATE INDEX IF NOT EXISTS idx_rwa_launches_enabled
    ON rwa_launches (source_code, base_symbol)
    WHERE enabled;

COMMENT ON TABLE rwa_launches IS
    'Primary-issuance (launchpad) state per RWA token; one surfaced launch per (source, token_addr).';
COMMENT ON COLUMN rwa_launches.price IS
    'Base-tier (undiscounted) price per token in quote_symbol units, decimals already applied.';
COMMENT ON COLUMN rwa_launches.progress_percent IS
    'total_bought / max_amount_cap * 100, precomputed to keep tiny values (e.g. 2.7e-7) intact.';
