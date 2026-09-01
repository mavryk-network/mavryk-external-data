package config

// EquiteezConfig holds Equiteez indexer (GraphQL/Hasura) client settings.
//
// The Cloudflare worker fronting the indexer injects the Hasura admin-secret and
// authorizes deployed callers by origin, so no secret is sent from here.
// IndexerPassword is a local/CI-only `?bypass=<secret>` fallback for callers
// without an allowed origin — leave it empty in deployed envs.
//
// RateLimit shares no quota with CoinGecko and defaults to 0 (disabled); the
// worker endpoint usually enforces no per-IP limit. Backfill: see ADR-0014.
type EquiteezConfig struct {
	IndexerURL      string                 `yaml:"indexer_url"`
	IndexerPassword string                 `yaml:"indexer_password"` // local/CI only: passed as ?bypass=<secret>
	RateLimit       RateLimitConfig        `yaml:"rate_limit"`
	Backfill        EquiteezBackfillConfig `yaml:"backfill"`
}

// EquiteezBackfillConfig controls the historical orderbook_order ingester.
//
// StartFrom ignores events with ended_at < this (RFC3339 or YYYY-MM-DD; empty =
// full history). JitterMs spreads the first tick across replicas. The backoff
// knobs have the same semantics as the CoinGecko backfill: exponential backoff
// per pair on transient errors, then a cooldown after BackfillMaxErrors.
type EquiteezBackfillConfig struct {
	Enabled           bool   `yaml:"enabled"`
	TickSeconds       int    `yaml:"tick_seconds"`
	BatchSize         int    `yaml:"batch_size"`
	StartFrom         string `yaml:"start_from"`
	JitterMs          int    `yaml:"jitter_ms"`
	BackfillMaxErrors int    `yaml:"backfill_max_errors"`
	BackoffInitialMs  int    `yaml:"backoff_initial_ms"`
	BackoffMaxMs      int    `yaml:"backoff_max_ms"`
	MaxBackoffMs      int    `yaml:"max_backoff_ms"`
}
