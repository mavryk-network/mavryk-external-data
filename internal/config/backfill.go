package config

// BackfillConfig controls the reverse-backfill job for FT prices (CoinGecko).
// The job walks oldest_ts backwards from the live cursor towards StartFrom, one
// ChunkMinutes-wide step per token per tick.
//
// MinStartFrom is a hard floor overriding any per-token start_from: reaching it
// disables the token with reason=reached_floor (sticky). After
// BackfillMaxErrors consecutive errors a token is parked for a cooldown, never
// permanently disabled. MaxBackoffMs hard-caps the computed backoff (24h) even
// if BackoffMaxMs is set absurdly high.
type BackfillConfig struct {
	Enabled           bool   `yaml:"enabled"`
	StartFrom         string `yaml:"start_from"`
	TickSeconds       int    `yaml:"tick_seconds"`
	JitterMs          int    `yaml:"jitter_ms"`
	ChunkMinutes      int    `yaml:"chunk_minutes"`
	MinStartFrom      string `yaml:"min_start_from"`
	BackfillMaxErrors int    `yaml:"backfill_max_errors"`
	BackoffInitialMs  int    `yaml:"backoff_initial_ms"`
	BackoffMaxMs      int    `yaml:"backoff_max_ms"`
	MaxBackoffMs      int    `yaml:"max_backoff_ms"`
}
