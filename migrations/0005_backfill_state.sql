-- 0005_backfill_state.sql
-- Per-(source, entity) backfill cursor + error/backoff bookkeeping.
-- entity_key is the FT token symbol or the RWA pair_id::text — both fit in text and
-- a single PK keyed off (source_code, entity_key) covers both domains.
--
-- Cursor representation:
--   * oldest_ts — used by reverse-time walkers (CoinGecko backfill); NULL for
--     forward-id walkers.
--   * cursor_id — used by forward-id walkers (Equiteez backfill walks
--     orderbook_order by serial id); NULL for reverse-time walkers.
-- A row populates exactly one of the two.

CREATE TABLE IF NOT EXISTS backfill_state (
    source_code      text        NOT NULL REFERENCES sources(code),
    entity_key       text        NOT NULL,
    oldest_ts        timestamptz,
    cursor_id        bigint,
    disabled         boolean     NOT NULL DEFAULT FALSE,
    disabled_reason  text        NOT NULL DEFAULT '',
    error_count      integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    next_attempt_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT NOW(),
    updated_at       timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (source_code, entity_key)
);

-- Hot path: pickReady-style query
--   WHERE source_code = $1 AND disabled = false AND (next_attempt_at IS NULL OR next_attempt_at <= NOW())
CREATE INDEX IF NOT EXISTS idx_backfill_state_due
    ON backfill_state (source_code, disabled, next_attempt_at);
