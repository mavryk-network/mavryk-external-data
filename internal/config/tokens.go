package config

import "time"

// TokenConfig holds per-token collector and backfill overrides.
//
// LiveLookbackSeconds is the maximum window the live job fetches per tick (the "safety
// overlap" after a missed tick). The live job never backfills history — that is the
// job of BackfillConfig. 0 means "use 2× interval_seconds" at resolution time.
type TokenConfig struct {
	IntervalSeconds     int                 `yaml:"interval_seconds"`       // Collection interval in seconds (0 = use global job.interval_seconds)
	Enabled             bool                `yaml:"enabled"`                // Enable/disable collection for this token (default: true)
	TimeoutSeconds      int                 `yaml:"timeout_seconds"`        // HTTP timeout in seconds (0 = use global api.timeout_seconds)
	MinTimeRangeSeconds int                 `yaml:"min_time_range_seconds"` // Minimum time range to collect (0 = use default 60)
	LiveLookbackSeconds int                 `yaml:"live_lookback_seconds"`  // Max window the live tick fetches; caps a stale last_ts. 0 = 2× interval.
	MaxChunkMinutes     int                 `yaml:"max_chunk_minutes"`      // Maximum chunk size for catch-up (0 = use backfill.chunk_minutes or default 60)
	Backfill            TokenBackfillConfig `yaml:"backfill"`               // Token-specific backfill settings
}

// TokenBackfillConfig holds per-token backfill overrides.
//
// SleepMs is deprecated — cadence is controlled globally by backfill.tick_seconds.
type TokenBackfillConfig struct {
	Enabled      bool   `yaml:"enabled"`       // Enable/disable backfill for this token (default: false, uses global backfill.enabled if not set)
	StartFrom    string `yaml:"start_from"`    // Backfill start date for this token (ISO date or RFC3339, overrides global if set)
	SleepMs      int    `yaml:"sleep_ms"`      // deprecated: replaced by backfill.tick_seconds; ignored at runtime
	ChunkMinutes int    `yaml:"chunk_minutes"` // Size of each backfill window in minutes (0 = use global backfill.chunk_minutes)
}

// GetJobInterval returns the global job interval as a duration.
func (c *Config) GetJobInterval() time.Duration {
	return time.Duration(c.Job.IntervalSeconds) * time.Second
}

// GetTokenConfig returns resolved token configuration (merging token-specific and global defaults).
func (c *Config) GetTokenConfig(tokenName string) TokenConfig {
	if c.Tokens == nil {
		c.Tokens = make(map[string]TokenConfig)
	}

	tokenCfg, exists := c.Tokens[tokenName]
	if !exists {
		return TokenConfig{
			IntervalSeconds:     0, // 0 means use global
			Enabled:             true,
			TimeoutSeconds:      0, // 0 means use global
			MinTimeRangeSeconds: 0, // 0 means use default 60
		}
	}

	if tokenCfg.IntervalSeconds == 0 {
		tokenCfg.IntervalSeconds = c.Job.IntervalSeconds
	}
	if tokenCfg.TimeoutSeconds == 0 {
		tokenCfg.TimeoutSeconds = c.API.TimeoutSeconds
	}
	if tokenCfg.MinTimeRangeSeconds == 0 {
		tokenCfg.MinTimeRangeSeconds = 60 // default 60 seconds
	}
	if tokenCfg.LiveLookbackSeconds == 0 {
		// Cap the live window at 2× interval so a stale last_ts after a DB wipe
		// doesn't force the live tick to pull hours of history in one request.
		tokenCfg.LiveLookbackSeconds = tokenCfg.IntervalSeconds * 2
		if tokenCfg.LiveLookbackSeconds == 0 {
			tokenCfg.LiveLookbackSeconds = 120 // belt-and-suspenders default
		}
	}
	if tokenCfg.MaxChunkMinutes == 0 {
		// Use backfill chunk size or default to 60 minutes
		if c.Backfill.ChunkMinutes > 0 {
			tokenCfg.MaxChunkMinutes = c.Backfill.ChunkMinutes
		} else {
			tokenCfg.MaxChunkMinutes = 60 // default 60 minutes
		}
	}

	// Fill in backfill defaults
	if tokenCfg.Backfill.SleepMs == 0 {
		tokenCfg.Backfill.SleepMs = c.Backfill.SleepMs
		if tokenCfg.Backfill.SleepMs == 0 {
			tokenCfg.Backfill.SleepMs = 3000 // default 3 seconds
		}
	}
	if tokenCfg.Backfill.ChunkMinutes == 0 {
		tokenCfg.Backfill.ChunkMinutes = c.Backfill.ChunkMinutes
		if tokenCfg.Backfill.ChunkMinutes == 0 {
			tokenCfg.Backfill.ChunkMinutes = 5 // default 5 minutes
		}
	}
	if tokenCfg.Backfill.StartFrom == "" {
		tokenCfg.Backfill.StartFrom = c.Backfill.StartFrom
	}

	return tokenCfg
}

// IsTokenBackfillEnabled checks if backfill is enabled for a specific token.
func (c *Config) IsTokenBackfillEnabled(tokenName string) bool {
	// First check if token collection is enabled
	if !c.IsTokenEnabled(tokenName) {
		return false // If token is disabled, backfill is also disabled
	}

	tokenCfg := c.GetTokenConfig(tokenName)

	// If backfill.enabled is explicitly false, disable backfill
	if !tokenCfg.Backfill.Enabled {
		return false
	}

	// If start_from is set, enable backfill (unless explicitly disabled above)
	if tokenCfg.Backfill.StartFrom != "" {
		return true
	}

	// If token-specific backfill.enabled is true, enable it
	if tokenCfg.Backfill.Enabled {
		return true
	}

	// Otherwise use global backfill setting
	return c.Backfill.Enabled
}

// GetTokenBackfillStartFrom returns the start date for token backfill.
func (c *Config) GetTokenBackfillStartFrom(tokenName string) string {
	tokenCfg := c.GetTokenConfig(tokenName)
	if tokenCfg.Backfill.StartFrom != "" {
		return tokenCfg.Backfill.StartFrom
	}
	return c.Backfill.StartFrom
}

// GetTokenInterval returns the collection interval for a token.
func (c *Config) GetTokenInterval(tokenName string) time.Duration {
	tokenCfg := c.GetTokenConfig(tokenName)
	return time.Duration(tokenCfg.IntervalSeconds) * time.Second
}

// GetTokenTimeout returns the HTTP client timeout for a token.
func (c *Config) GetTokenTimeout(tokenName string) time.Duration {
	tokenCfg := c.GetTokenConfig(tokenName)
	return time.Duration(tokenCfg.TimeoutSeconds) * time.Second
}

// GetTokenLiveLookback returns the live-job lookback window for a token.
func (c *Config) GetTokenLiveLookback(tokenName string) time.Duration {
	tokenCfg := c.GetTokenConfig(tokenName)
	return time.Duration(tokenCfg.LiveLookbackSeconds) * time.Second
}

// IsTokenEnabled reports whether collection is enabled for the token.
func (c *Config) IsTokenEnabled(tokenName string) bool {
	tokenCfg := c.GetTokenConfig(tokenName)
	return tokenCfg.Enabled
}
