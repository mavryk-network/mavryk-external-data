-- 0020_rwa_launches_disabled_reason.sql
-- Records WHO disabled a launch, mirroring rwa_pairs.disabled_reason (0019):
-- lets the sync retire delisted launches and re-enable them on reappearance,
-- while operator disables (reason NULL/other) survive every sync.

ALTER TABLE rwa_launches ADD COLUMN IF NOT EXISTS disabled_reason TEXT;
