package config

// BackfillConfig controls the reverse-backfill job: one "step" per tick walks the
// oldest_ts cursor backwards from now() to start_from (clamped by min_start_from).
//
// Knobs:
//   - TickSeconds:         cadence of the backfill ticker; total outbound RPS is bounded by
//                          (step requests) / TickSeconds, independent of the live job.
//   - ChunkMinutes:        width of a single step window.
//   - MinStartFrom:        hard floor that overrides any per-token start_from — once the
//                          cursor reaches this value, the token is marked disabled with
//                          reason=reached_start_from (sticky).
//   - BackfillMaxErrors:   after this many consecutive CoinGecko errors the token is auto-
//                          disabled (reason=auto_disabled); the flag is persisted in
//                          backfill_state so a restart does not re-flood the API.
//   - BackoffInitialMs /
//     BackoffMaxMs:        exponential backoff per-token on transient errors.
//   - SleepMs:             deprecated. Kept only to avoid breaking existing .env files; the
//                          new model expresses rate via TickSeconds. Ignored at runtime.
type BackfillConfig struct {
	Enabled           bool   `yaml:"enabled"`
	StartFrom         string `yaml:"start_from"`          // ISO date or RFC3339 (e.g., 2025-09-18 or 2025-09-18T00:00:00Z)
	TickSeconds       int    `yaml:"tick_seconds"`        // cadence of the backfill ticker (seconds)
	ChunkMinutes      int    `yaml:"chunk_minutes"`       // size of each backfill window in minutes
	MinStartFrom      string `yaml:"min_start_from"`      // optional hard floor; ISO date or RFC3339
	BackfillMaxErrors int    `yaml:"backfill_max_errors"` // consecutive errors before auto-disable
	BackoffInitialMs  int    `yaml:"backoff_initial_ms"`  // first retry delay on error
	BackoffMaxMs      int    `yaml:"backoff_max_ms"`      // cap for exponential backoff
	SleepMs           int    `yaml:"sleep_ms"`            // deprecated: replaced by tick_seconds; ignored at runtime
}
