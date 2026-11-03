-- CRM versions and valuation revisions are durable, never reset by retention.
ALTER TABLE equiteez_allowlist ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS rwa_allowlist_versions (
 url TEXT NOT NULL, version TEXT NOT NULL, payload JSONB NOT NULL,
 first_seen TIMESTAMPTZ NOT NULL, last_seen TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(url,first_seen)
);
-- A pre-migration snapshot proves scope only from its recorded fetched_at.
INSERT INTO rwa_allowlist_versions(url,version,payload,first_seen,last_seen)
 SELECT url,md5(payload::text),payload,fetched_at,fetched_at FROM equiteez_allowlist
 ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS rwa_valuation_current (
 network TEXT NOT NULL, contract TEXT NOT NULL, token_id TEXT NOT NULL,
 revision BIGINT NOT NULL DEFAULT 0 CHECK(revision>=0),
 fingerprint TEXT NOT NULL DEFAULT '', snapshot JSONB,
 as_of TIMESTAMPTZ, PRIMARY KEY(network,contract,token_id)
);

CREATE TABLE IF NOT EXISTS rwa_supply_observations (
 observed_at TIMESTAMPTZ NOT NULL, observation_id TEXT NOT NULL,
 network TEXT NOT NULL, contract TEXT NOT NULL, token_id TEXT NOT NULL,
 block_level BIGINT NOT NULL, block_hash TEXT NOT NULL, scope_version TEXT NOT NULL,
 total_supply NUMERIC, excluded_supply NUMERIC, circulating_supply NUMERIC,
 snapshot JSONB NOT NULL, invalidated BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(observed_at,observation_id)
);
SELECT create_hypertable('rwa_supply_observations','observed_at',if_not_exists=>TRUE);
CREATE INDEX IF NOT EXISTS rwa_supply_identity_time ON rwa_supply_observations(network,contract,token_id,observed_at DESC);

CREATE TABLE IF NOT EXISTS rwa_valuation_points (
 as_of TIMESTAMPTZ NOT NULL, point_id TEXT NOT NULL,
 network TEXT NOT NULL, contract TEXT NOT NULL, token_id TEXT NOT NULL,
 symbol TEXT NOT NULL, quote_contract TEXT NOT NULL, quote_token_id TEXT NOT NULL,
 observation_id TEXT NOT NULL, block_level BIGINT NOT NULL, block_hash TEXT NOT NULL,
 market_cap NUMERIC, fdv NUMERIC, snapshot JSONB NOT NULL,
 invalidated BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(as_of,point_id)
);
SELECT create_hypertable('rwa_valuation_points','as_of',if_not_exists=>TRUE);
CREATE INDEX IF NOT EXISTS rwa_valuation_identity_time ON rwa_valuation_points(network,contract,token_id,symbol,as_of DESC);

-- Preserve the closing observation ID, including unavailable observations;
-- selecting max(market_cap) or filtering nulls would silently bridge gaps.
CREATE MATERIALIZED VIEW IF NOT EXISTS rwa_valuation_1m WITH (timescaledb.continuous) AS
 SELECT time_bucket('1 minute',as_of) bucket,network,contract,token_id,symbol,
 last(point_id,as_of) point_id,max(as_of) as_of
 FROM rwa_valuation_points GROUP BY 1,2,3,4,5 WITH NO DATA;
CREATE MATERIALIZED VIEW IF NOT EXISTS rwa_valuation_1h WITH (timescaledb.continuous) AS
 SELECT time_bucket('1 hour',as_of) bucket,network,contract,token_id,symbol,
 last(point_id,as_of) point_id,max(as_of) as_of
 FROM rwa_valuation_points GROUP BY 1,2,3,4,5 WITH NO DATA;
CREATE MATERIALIZED VIEW IF NOT EXISTS rwa_valuation_1d WITH (timescaledb.continuous) AS
 SELECT time_bucket('1 day',as_of) bucket,network,contract,token_id,symbol,
 last(point_id,as_of) point_id,max(as_of) as_of
 FROM rwa_valuation_points GROUP BY 1,2,3,4,5 WITH NO DATA;
ALTER MATERIALIZED VIEW rwa_valuation_1m SET (timescaledb.materialized_only=false);
ALTER MATERIALIZED VIEW rwa_valuation_1h SET (timescaledb.materialized_only=false);
ALTER MATERIALIZED VIEW rwa_valuation_1d SET (timescaledb.materialized_only=false);
SELECT add_continuous_aggregate_policy('rwa_valuation_1m',start_offset=>INTERVAL '7 days',end_offset=>INTERVAL '1 minute',schedule_interval=>INTERVAL '1 minute',if_not_exists=>TRUE);
SELECT add_continuous_aggregate_policy('rwa_valuation_1h',start_offset=>INTERVAL '30 days',end_offset=>INTERVAL '1 hour',schedule_interval=>INTERVAL '1 hour',if_not_exists=>TRUE);
SELECT add_continuous_aggregate_policy('rwa_valuation_1d',start_offset=>INTERVAL '90 days',end_offset=>INTERVAL '1 day',schedule_interval=>INTERVAL '1 day',if_not_exists=>TRUE);

-- Retain the joined provenance as long as any aggregate references it. Compress
-- raw history, but do not drop it and leave dangling aggregate observation IDs.
ALTER TABLE rwa_supply_observations SET (timescaledb.compress,timescaledb.compress_segmentby='network,contract,token_id');
ALTER TABLE rwa_valuation_points SET (timescaledb.compress,timescaledb.compress_segmentby='network,contract,token_id,symbol');
SELECT add_compression_policy('rwa_supply_observations',INTERVAL '30 days',if_not_exists=>TRUE);
SELECT add_compression_policy('rwa_valuation_points',INTERVAL '30 days',if_not_exists=>TRUE);

CREATE TABLE IF NOT EXISTS rwa_valuation_backfill (
 job_id TEXT PRIMARY KEY, network TEXT NOT NULL, contract TEXT NOT NULL, token_id TEXT NOT NULL,
 from_level BIGINT NOT NULL,
 next_level BIGINT NOT NULL, through_level BIGINT NOT NULL, step BIGINT NOT NULL,
 refreshed_through_level BIGINT NOT NULL DEFAULT 0,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), complete BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS rwa_valuation_backfill_results (
 job_id TEXT NOT NULL REFERENCES rwa_valuation_backfill(job_id), level BIGINT NOT NULL,
 as_of TIMESTAMPTZ, reason TEXT NOT NULL, PRIMARY KEY(job_id,level)
);

CREATE TABLE IF NOT EXISTS rwa_valuation_attempts (
 network TEXT NOT NULL,contract TEXT NOT NULL,reason TEXT NOT NULL,
 attempted_at TIMESTAMPTZ NOT NULL,PRIMARY KEY(network,contract)
);
