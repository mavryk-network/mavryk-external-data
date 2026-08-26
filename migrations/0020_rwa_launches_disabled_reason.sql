-- 0020_rwa_launches_disabled_reason.sql
-- Records WHO disabled a launch, mirroring rwa_pairs.disabled_reason (0019).
--
-- Why: the launch sync was upsert-only — a launch delisted upstream (or one
-- whose price broke) kept serving enabled=true, "Active", at its last stored
-- price forever. With the reason recorded the sync can soft-disable launches
-- missing from a complete upstream view and re-enable them if they reappear,
-- while operator disables (reason NULL/other) survive every sync.

ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS disabled_reason TEXT;
