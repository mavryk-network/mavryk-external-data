-- Preserve all identities, history and operator switches. Legacy enabled=false
-- is ambiguous; only explicitly selected contracts may be recovered later.
BEGIN;
ALTER TABLE rwa_pairs ADD COLUMN IF NOT EXISTS in_allowlist BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE rwa_pairs ADD COLUMN IF NOT EXISTS indexer_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS in_allowlist BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS indexer_generation TEXT NOT NULL DEFAULT '';
ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS launchpad_addr TEXT NOT NULL DEFAULT '';
ALTER TABLE backfill_state ADD COLUMN IF NOT EXISTS indexer_generation TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS equiteez_allowlist (
 id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
 url TEXT NOT NULL,
 payload JSONB NOT NULL,
 fetched_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS equiteez_recovery_applied (
 kind TEXT NOT NULL CHECK (kind IN ('orderbook','launch')),
 address TEXT NOT NULL,
 applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(kind,address)
);
COMMIT;
