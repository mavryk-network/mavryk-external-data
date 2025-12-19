-- +migrate Up
-- Rename table mev.quotes -> mev.mvrk

ALTER TABLE IF EXISTS mev.quotes
RENAME TO mvrk;

ALTER INDEX IF EXISTS mev.idx_mev_quotes_timestamp
RENAME TO idx_mev_mvrk_timestamp;

ALTER INDEX IF EXISTS mev.idx_mev_quotes_timestamp_desc
RENAME TO idx_mev_mvrk_timestamp_desc;

ALTER INDEX IF EXISTS mev.idx_mev_quotes_deleted_at
RENAME TO idx_mev_mvrk_deleted_at;

ALTER TABLE mev.mvrk
RENAME CONSTRAINT quotes_pkey TO mvrk_pkey;
