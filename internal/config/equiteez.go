package config

// EquiteezConfig holds Equiteez indexer (GraphQL/Hasura) client settings.
//
// Auth lives in the equiteez Cloudflare worker fronting the indexer: it injects
// the Hasura admin-secret itself and authorizes deployed (in-cluster) callers by
// origin/domain, so the backend sends no secret there. IndexerPassword is a
// LOCAL/CI-only bypass: when set, the client appends it to the request URL as
// `?bypass=<secret>` (the worker accepts that in place of an allowed origin). Leave
// it empty in deployed envs. See interactions/equiteez/client.go.
//
// RateLimit is independent from the CoinGecko one — the two services share no
// quota. The worker endpoint usually has no per-IP limit, so this defaults to 0
// (disabled). Set equiteez.rate_limit.rps > 0 only if you know it enforces a quota.
//
// Backfill controls the one-shot historical fetch from `orderbook_order` events
// — see jobs/equiteez_backfill.go and ADR-0014.
type EquiteezConfig struct {
	IndexerURL      string                 `yaml:"indexer_url"`
	IndexerPassword string                 `yaml:"indexer_password"` // local/CI only: passed as ?bypass=<secret>
	RateLimit       RateLimitConfig        `yaml:"rate_limit"`
	Backfill        EquiteezBackfillConfig `yaml:"backfill"`
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
//     on transient errors, hard auto-disable after BackfillMaxErrors.
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
