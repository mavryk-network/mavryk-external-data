package config

import "time"

// TokenConfig holds per-token collector and backfill overrides.
//
// Token names (map keys) are validated against the runtime token registry
// (loaded from the `tokens` table) at job-start time, not at config-load time.
type TokenConfig struct {
	IntervalSeconds     int                 `yaml:"interval_seconds"`
	Enabled             bool                `yaml:"enabled"`
	TimeoutSeconds      int                 `yaml:"timeout_seconds"`
	MinTimeRangeSeconds int                 `yaml:"min_time_range_seconds"`
	LiveLookbackSeconds int                 `yaml:"live_lookback_seconds"`
	MaxChunkMinutes     int                 `yaml:"max_chunk_minutes"`
	Backfill            TokenBackfillConfig `yaml:"backfill"`
}

// TokenBackfillConfig holds per-token backfill overrides.
type TokenBackfillConfig struct {
	Enabled      bool   `yaml:"enabled"`
	StartFrom    string `yaml:"start_from"`
	ChunkMinutes int    `yaml:"chunk_minutes"`
}

// GetJobInterval returns the global job interval as a duration.
func (c *Config) GetJobInterval() time.Duration {
	return time.Duration(c.Job.IntervalSeconds) * time.Second
}

// GetTokenConfig returns resolved token configuration (merging token-specific and global defaults).
// Defaults are applied in-place (return is by-value so the receiver is not mutated).
func (c *Config) GetTokenConfig(tokenName string) TokenConfig {
	if c.Tokens == nil {
		c.Tokens = make(map[string]TokenConfig)
	}

	tokenCfg, exists := c.Tokens[tokenName]
	if !exists {
		// Token not explicitly configured. Default to "enabled with global cadences."
		tokenCfg = TokenConfig{Enabled: true}
	}

	if tokenCfg.IntervalSeconds == 0 {
		tokenCfg.IntervalSeconds = c.Job.IntervalSeconds
	}
	if tokenCfg.TimeoutSeconds == 0 {
		tokenCfg.TimeoutSeconds = c.API.TimeoutSeconds
	}
	if tokenCfg.MinTimeRangeSeconds == 0 {
		tokenCfg.MinTimeRangeSeconds = 60
	}
	if tokenCfg.LiveLookbackSeconds == 0 {
		// 2× interval covers a missed tick — but CoinGecko's market_chart/range
		// has 5-minute granularity for windows ≤ 1 day, so a 120s window for an
		// interval=60s token systematically misses bucket boundaries on
		// low-liquidity coins (returns `prices: []`). Floor the default at 600s
		// (10 min = 2 CG buckets) so the live tick always overlaps at least one
		// data point. Operators can still override per-token to lower this when
		// dealing with high-frequency upstreams.
		const minLookback = 600
		tokenCfg.LiveLookbackSeconds = tokenCfg.IntervalSeconds * 2
		if tokenCfg.LiveLookbackSeconds < minLookback {
			tokenCfg.LiveLookbackSeconds = minLookback
		}
	}
	if tokenCfg.MaxChunkMinutes == 0 {
		if c.Backfill.ChunkMinutes > 0 {
			tokenCfg.MaxChunkMinutes = c.Backfill.ChunkMinutes
		} else {
			tokenCfg.MaxChunkMinutes = 60
		}
	}

	if tokenCfg.Backfill.ChunkMinutes == 0 {
		tokenCfg.Backfill.ChunkMinutes = c.Backfill.ChunkMinutes
		if tokenCfg.Backfill.ChunkMinutes == 0 {
			tokenCfg.Backfill.ChunkMinutes = 5
		}
	}
	if tokenCfg.Backfill.StartFrom == "" {
		tokenCfg.Backfill.StartFrom = c.Backfill.StartFrom
	}

	return tokenCfg
}

// IsTokenBackfillEnabled reports whether backfill should run for a token.
//
// Semantics (refactoring follow-up — refactoring_v2 §2.2 spirit):
//   - global `backfill.enabled: false` is a kill-switch — when off, no token
//     runs backfill, regardless of per-token overrides. This matches the
//     operator-friendly intent of `BACKFILL_ENABLED=false` as an emergency
//     stop. Switching on per-token semantics would surprise ops.
//   - global `job.enabled: false` (or per-token `enabled: false`) also
//     disables backfill: backfill needs the live anchor to seed `oldest_ts`.
//   - per-token `backfill.enabled: false` opts that token out even when
//     global is on.
//   - per-token `start_from` is required for backfill to do anything; missing
//     start_from falls back to global, missing-both = no-op.
func (c *Config) IsTokenBackfillEnabled(tokenName string) bool {
	if !c.Backfill.Enabled {
		return false
	}
	if !c.IsTokenEnabled(tokenName) {
		return false
	}
	tokenCfg, exists := c.Tokens[tokenName]
	if !exists {
		return c.Backfill.StartFrom != ""
	}
	if !tokenCfg.Backfill.Enabled {
		return false
	}
	if tokenCfg.Backfill.StartFrom != "" {
		return true
	}
	return c.Backfill.StartFrom != ""
}

// GetTokenBackfillStartFrom returns the resolved start date for token backfill.
func (c *Config) GetTokenBackfillStartFrom(tokenName string) string {
	tc := c.GetTokenConfig(tokenName)
	if tc.Backfill.StartFrom != "" {
		return tc.Backfill.StartFrom
	}
	return c.Backfill.StartFrom
}

// GetTokenInterval returns the collection interval for a token as a duration.
func (c *Config) GetTokenInterval(tokenName string) time.Duration {
	return time.Duration(c.GetTokenConfig(tokenName).IntervalSeconds) * time.Second
}

// GetTokenTimeout returns the HTTP client timeout for a token.
func (c *Config) GetTokenTimeout(tokenName string) time.Duration {
	return time.Duration(c.GetTokenConfig(tokenName).TimeoutSeconds) * time.Second
}

// GetTokenLiveLookback returns the live-job lookback window for a token.
func (c *Config) GetTokenLiveLookback(tokenName string) time.Duration {
	return time.Duration(c.GetTokenConfig(tokenName).LiveLookbackSeconds) * time.Second
}

// IsTokenEnabled reports whether collection is enabled for the token.
// A token absent from c.Tokens defaults to enabled when the live job runs.
func (c *Config) IsTokenEnabled(tokenName string) bool {
	tokenCfg, exists := c.Tokens[tokenName]
	if !exists {
		return true
	}
	return tokenCfg.Enabled
}
