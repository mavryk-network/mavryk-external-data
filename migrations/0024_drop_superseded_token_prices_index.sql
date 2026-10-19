-- Never remove the working index after an incomplete build or when this file
-- is run out of order. The runners stop on an error; the next full run repairs
-- an invalid replacement in 0022 and retries 0023 before reaching this step.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_index
        WHERE indexrelid = to_regclass('idx_token_prices_latest_source')
          AND indrelid = 'token_prices'::regclass
          AND indisvalid AND indisready
    ) THEN
        RAISE EXCEPTION 'token_prices latest-source index is missing or invalid; rerun migrations before retiring the legacy index';
    END IF;
    DROP INDEX IF EXISTS idx_token_prices_latest;
END $$;
