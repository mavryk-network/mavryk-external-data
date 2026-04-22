ALTER TABLE mev.mvrk ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP WITH TIME ZONE;
ALTER TABLE mev.usdt ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMP WITH TIME ZONE;

CREATE INDEX IF NOT EXISTS idx_mev_mvrk_deleted_at ON mev.mvrk (deleted_at);
CREATE INDEX IF NOT EXISTS idx_mev_usdt_deleted_at ON mev.usdt (deleted_at);
