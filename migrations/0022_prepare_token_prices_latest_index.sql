-- An interrupted transaction_per_chunk build can leave an INVALID parent
-- index. IF NOT EXISTS alone would skip that index forever, so discard only
-- that incomplete replacement before retrying 0023. The working legacy index
-- remains available throughout recovery.
-- https://www.tigerdata.com/docs/reference/timescaledb/hypertables/create_index
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_token_prices_latest_source')
          AND indrelid = 'token_prices'::regclass
          AND NOT indisvalid
    ) THEN
        DROP INDEX idx_token_prices_latest_source;
    END IF;
END $$;
