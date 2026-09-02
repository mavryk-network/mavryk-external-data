-- 0019_rwa_pairs_disabled_reason.sql
-- Records WHO disabled a pair: sync disables ('sync_missing') are re-enabled
-- when the pair reappears upstream; operator disables (NULL/other) survive.

ALTER TABLE rwa_pairs ADD COLUMN IF NOT EXISTS disabled_reason TEXT;
