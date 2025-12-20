-- Rename table mev.quotes -> mev.mvrk

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 
        FROM information_schema.tables 
        WHERE table_schema = 'mev' 
        AND table_name = 'quotes'
    ) THEN
        ALTER TABLE mev.quotes RENAME TO mvrk;
    END IF;

    IF EXISTS (
        SELECT 1 
        FROM pg_indexes 
        WHERE schemaname = 'mev' 
        AND indexname = 'idx_mev_quotes_timestamp'
    ) THEN
        ALTER INDEX mev.idx_mev_quotes_timestamp RENAME TO idx_mev_mvrk_timestamp;
    END IF;

    IF EXISTS (
        SELECT 1 
        FROM pg_indexes 
        WHERE schemaname = 'mev' 
        AND indexname = 'idx_mev_quotes_timestamp_desc'
    ) THEN
        ALTER INDEX mev.idx_mev_quotes_timestamp_desc RENAME TO idx_mev_mvrk_timestamp_desc;
    END IF;

    IF EXISTS (
        SELECT 1 
        FROM pg_indexes 
        WHERE schemaname = 'mev' 
        AND indexname = 'idx_mev_quotes_deleted_at'
    ) THEN
        ALTER INDEX mev.idx_mev_quotes_deleted_at RENAME TO idx_mev_mvrk_deleted_at;
    END IF;

    IF EXISTS (
        SELECT 1 
        FROM pg_constraint 
        WHERE conname = 'quotes_pkey' 
        AND connamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'mev')
    ) AND NOT EXISTS (
        SELECT 1 
        FROM pg_constraint 
        WHERE conname = 'mvrk_pkey' 
        AND connamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'mev')
    ) THEN
        IF EXISTS (
            SELECT 1 
            FROM information_schema.tables 
            WHERE table_schema = 'mev' 
            AND table_name = 'mvrk'
        ) THEN
            ALTER TABLE mev.mvrk RENAME CONSTRAINT quotes_pkey TO mvrk_pkey;
        ELSIF EXISTS (
            SELECT 1 
            FROM information_schema.tables 
            WHERE table_schema = 'mev' 
            AND table_name = 'quotes'
        ) THEN
            ALTER TABLE mev.quotes RENAME CONSTRAINT quotes_pkey TO mvrk_pkey;
        END IF;
    END IF;
END $$;