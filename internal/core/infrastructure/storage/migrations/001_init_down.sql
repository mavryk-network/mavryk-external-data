-- Down migration for 001_init.sql
-- Drops table and schema created by the up migration.

-- Drop quotes table (hypertable) if exists
DROP TABLE IF EXISTS mev.quotes CASCADE;

-- Drop schema if empty
DROP SCHEMA IF EXISTS mev CASCADE;

-- Note: We intentionally do not drop the timescaledb extension
-- because it may be used by other schemas/databases.


