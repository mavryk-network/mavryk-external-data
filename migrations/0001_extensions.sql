-- 0001_extensions.sql
-- Activate TimescaleDB. The extension is preinstalled in the timescale/timescaledb image
-- used by docker-compose; on plain Postgres the CREATE EXTENSION will fail and the rest
-- of the schema will run on a regular table.
CREATE EXTENSION IF NOT EXISTS timescaledb;
