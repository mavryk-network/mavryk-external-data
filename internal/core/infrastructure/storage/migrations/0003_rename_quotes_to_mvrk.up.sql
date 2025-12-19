-- +migrate Up
-- Rename table mev.quotes -> mev.mvrk

DO $$
BEGIN
    -- Skip if table mvrk already exists (migration already applied)
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_schema = 'mev' AND table_name = 'mvrk'
    ) AND EXISTS (
        SELECT 1 FROM information_schema.tables 
        WHERE table_schema = 'mev' AND table_name = 'quotes'
    ) THEN
        -- Rename table (indexes and constraints are automatically renamed)
        ALTER TABLE mev.quotes RENAME TO mvrk;
        
        -- Explicitly rename indexes to match expected names
        BEGIN
            ALTER INDEX mev.idx_mev_quotes_timestamp RENAME TO idx_mev_mvrk_timestamp;
        EXCEPTION WHEN undefined_object THEN
            NULL; -- Index already renamed or doesn't exist
        END;
        
        BEGIN
            ALTER INDEX mev.idx_mev_quotes_timestamp_desc RENAME TO idx_mev_mvrk_timestamp_desc;
        EXCEPTION WHEN undefined_object THEN
            NULL;
        END;
        
        BEGIN
            ALTER INDEX mev.idx_mev_quotes_deleted_at RENAME TO idx_mev_mvrk_deleted_at;
        EXCEPTION WHEN undefined_object THEN
            NULL;
        END;
        
        -- Rename constraint
        BEGIN
            ALTER TABLE mev.mvrk RENAME CONSTRAINT quotes_pkey TO mvrk_pkey;
        EXCEPTION WHEN undefined_object THEN
            NULL; -- Constraint already renamed or doesn't exist
        END;
    END IF;
END $$ LANGUAGE plpgsql;
