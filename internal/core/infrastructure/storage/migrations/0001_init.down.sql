-- +migrate Down
-- Drop quotes table (hypertable) if exists
DROP TABLE IF EXISTS mev.quotes CASCADE;

-- Drop schema if empty
DROP SCHEMA IF EXISTS mev CASCADE;
