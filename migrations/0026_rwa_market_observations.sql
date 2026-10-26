-- Keep complete pricing evidence for exact-time enrichment of legacy last rows.
-- Do not seed from latest: older legacy/backfill points have no such evidence.
CREATE TABLE IF NOT EXISTS rwa_market_observations (
    LIKE rwa_market_snapshots INCLUDING DEFAULTS INCLUDING CONSTRAINTS,
    PRIMARY KEY (pair_id, observed_at),
    FOREIGN KEY (pair_id) REFERENCES rwa_pairs(id)
);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        RAISE NOTICE 'TimescaleDB not installed; pricing observations stay a regular table without automatic retention';
        RETURN;
    END IF;
    PERFORM create_hypertable('rwa_market_observations', 'observed_at',
        chunk_time_interval => INTERVAL '1 day', if_not_exists => TRUE);
    IF NOT EXISTS (
        SELECT 1 FROM timescaledb_information.compression_settings
        WHERE hypertable_name = 'rwa_market_observations'
    ) THEN
        ALTER TABLE rwa_market_observations SET (
            timescaledb.compress,
            timescaledb.compress_segmentby = 'pair_id',
            timescaledb.compress_orderby = 'observed_at DESC'
        );
    END IF;
    PERFORM add_compression_policy('rwa_market_observations', INTERVAL '7 days', if_not_exists => TRUE);
    PERFORM add_retention_policy('rwa_market_observations', INTERVAL '2 years', if_not_exists => TRUE);
END $$ LANGUAGE plpgsql;
