-- Quotes are append-only time series; soft-delete is not used.

DROP INDEX IF EXISTS mev.idx_mev_mvrk_deleted_at;
DROP INDEX IF EXISTS mev.idx_mev_usdt_deleted_at;

ALTER TABLE mev.mvrk DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE mev.usdt DROP COLUMN IF EXISTS deleted_at;
