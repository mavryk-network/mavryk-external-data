-- 0019_rwa_pairs_disabled_reason.sql
-- Records WHO disabled a pair, so the discovery sync can undo its own work.
--
-- Why: DisableMissingRWAPairs soft-disables pairs absent from the upstream
-- allowlist, but a partial (non-empty) indexer view disabled healthy pairs
-- permanently — UpsertRWAPair never re-enabled rows, and without a reason
-- column a sync disable was indistinguishable from an operator's choice.
-- With the reason recorded, the next sync that sees the pair again re-enables
-- it; operator disables (reason NULL or anything else) survive untouched.

ALTER TABLE rwa_pairs ADD COLUMN IF NOT EXISTS disabled_reason TEXT;

COMMENT ON COLUMN rwa_pairs.disabled_reason IS
    'Why enabled=false: ''sync_missing'' = auto-disabled by the discovery sync (re-enabled automatically when the pair reappears upstream); NULL/other = operator decision, never touched by the sync.';
