-- One row per timestamp per hypertable (token lives in table name).
-- Removes duplicate timestamps (keeps the row with the smallest id), then adds a unique index
-- so INSERT ... ON CONFLICT (timestamp) DO NOTHING works for collectors/backfill.

DELETE FROM mev.mvrk a
WHERE EXISTS (
    SELECT 1 FROM mev.mvrk b
    WHERE b.timestamp = a.timestamp AND b.id < a.id
);

DELETE FROM mev.usdt a
WHERE EXISTS (
    SELECT 1 FROM mev.usdt b
    WHERE b.timestamp = a.timestamp AND b.id < a.id
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_mev_mvrk_timestamp ON mev.mvrk (timestamp);
CREATE UNIQUE INDEX IF NOT EXISTS uq_mev_usdt_timestamp ON mev.usdt (timestamp);
