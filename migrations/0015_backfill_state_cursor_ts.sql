-- 0015_backfill_state_cursor_ts.sql
-- Fill-time cursor for forward-walking backfills (Equiteez orderbook_order).
--
-- Why: `orderbook_order.id` is assigned when an order is CREATED, not when it
-- fills. Paginating by id alone permanently skips a long-resting limit order
-- that fills after the cursor has already moved past its id — its trade never
-- lands in rwa_quote_prices, and the live collector cannot recover it (it
-- snapshots bid/ask/last, it does not replay the event log).
--
-- Walking by (ended_at, id) — fill time, with id as the tie-break for fills
-- sharing a timestamp — cannot miss a late fill: a fill's ended_at is always
-- >= the cursor position at the time it happens.
--
-- Migration semantics: existing rows get cursor_ts = NULL, which the job reads
-- as "no fill-time cursor yet" and restarts the walk from backfill.start_from.
-- That one-off re-walk is exactly what recovers the fills the id-only cursor
-- skipped; it is safe because rwa_quote_prices upserts on (pair_id, side, ts).

ALTER TABLE backfill_state ADD COLUMN IF NOT EXISTS cursor_ts TIMESTAMPTZ;

COMMENT ON COLUMN backfill_state.cursor_ts IS
    'Fill-time keyset cursor: ended_at of the last ingested order. Paired with cursor_id as the tie-break. NULL = restart the walk from start_from.';
