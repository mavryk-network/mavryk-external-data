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
	AllowlistURL        string                 `yaml:"allowlist_url"`
	HeadMaxAgeSeconds   int                    `yaml:"head_max_age_seconds"`
	RequiredIndexes     []string               `yaml:"required_indexes"`
	RecoverOrderbooks   []string               `yaml:"recover_orderbooks"`
	RecoverLaunchTokens []string               `yaml:"recover_launch_tokens"`
	IndexerURL          string                 `yaml:"indexer_url"`
	IndexerPassword     string                 `yaml:"indexer_password"` // local/CI only: passed as ?bypass=<secret>
	RateLimit           RateLimitConfig        `yaml:"rate_limit"`
	Backfill            EquiteezBackfillConfig `yaml:"backfill"`
}

// EquiteezBackfillConfig controls the historical orderbook_order ingester.
//
// Knobs:
//   - Enabled:    opt-in master switch. Off by default.
//   - TickSeconds: cadence of the backfill ticker (default 30s — indexer is
//     usually local, so an aggressive cadence is cheap).
//   - BatchSize:  number of orderbook_order rows fetched per pair per tick.
//   - StartFrom:  ignore events with ended_at < this (RFC3339 or YYYY-MM-DD).
//     Empty = full history. Useful to bound the initial sweep when the indexer
//     contains years of data.
//   - JitterMs:   random ± offset on the first tick to avoid synchronized
//     hammering across replicas. Default 1000.
//   - BackfillMaxErrors / BackoffInitialMs / BackoffMaxMs / MaxBackoffMs:
//     same semantics as the CoinGecko backfill — exponential backoff per pair
//     on errors. BackfillMaxErrors is deprecated for Equiteez: errors always retry.
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
