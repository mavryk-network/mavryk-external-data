-- Migration: 002_backfill_state.sql
-- Per-token persistent state for the reverse-backfill job.
-- oldest_ts: kept as a cache of MIN(quotes.timestamp) WHERE token = $1, updated after each
--           successful chunk. NULL = not yet initialized (bootstrap).
-- disabled + disabled_reason: sticky flags. reason ∈ {'reached_start_from','auto_disabled','manual'}.
-- error_count + last_error + next_attempt_at: exponential-backoff bookkeeping (reset on success).

CREATE TABLE IF NOT EXISTS backfill_state (
    token            text        PRIMARY KEY,
    oldest_ts        timestamptz,
    disabled         boolean     NOT NULL DEFAULT FALSE,
    disabled_reason  text        NOT NULL DEFAULT '',
    error_count      integer     NOT NULL DEFAULT 0,
    last_error       text        NOT NULL DEFAULT '',
    next_attempt_at  timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_backfill_state_next_attempt_at
    ON backfill_state (next_attempt_at)
    WHERE next_attempt_at IS NOT NULL;
