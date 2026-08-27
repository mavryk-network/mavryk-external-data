package config

// BackfillConfig controls the reverse-backfill job for FT prices (CoinGecko).
//
// The job walks oldest_ts backwards from the live cursor towards start_from
// (clamped by min_start_from). One step per token per tick.
//
// Knobs:
//   - TickSeconds:          cadence of the backfill ticker; total outbound RPS is bounded
//     by (step requests) / TickSeconds, independent of the live job.
//   - JitterMs:             random ±jitter added to the first tick to avoid replicas
//     hammering the upstream in lockstep.
//   - ChunkMinutes:         width of a single step window.
//   - MinStartFrom:         hard floor that overrides any per-token start_from — once the
//     cursor reaches this value, the token is marked disabled with
//     reason=reached_floor (sticky).
//   - BackfillMaxErrors:    after this many consecutive errors the token is parked for a
//     cooldown (never permanently disabled — a transient outage must not need ops SQL).
//   - BackoffInitialMs /
//     BackoffMaxMs:         exponential backoff per-token on transient errors.
//   - MaxBackoffMs:         hard cap on the computed backoff (default 24h). Keeps a
//     failing token's "next attempt" within an operationally sane
//     bound even if BackoffMaxMs is mistakenly set very high.
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
