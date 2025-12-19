-- +migrate Down
-- Rename table mev.mvrk -> mev.quotes

ALTER TABLE IF EXISTS mev.mvrk
RENAME TO quotes;

ALTER INDEX IF EXISTS mev.idx_mev_mvrk_timestamp
RENAME TO idx_mev_quotes_timestamp;

ALTER INDEX IF EXISTS mev.idx_mev_mvrk_timestamp_desc
RENAME TO idx_mev_quotes_timestamp_desc;

ALTER INDEX IF EXISTS mev.idx_mev_mvrk_deleted_at
RENAME TO idx_mev_quotes_deleted_at;

ALTER TABLE mev.quotes
RENAME CONSTRAINT mvrk_pkey TO quotes_pkey;
