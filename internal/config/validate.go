package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"quotes/internal/core/domain/quotes"
)

// Validate checks required fields and formats after defaults and env overrides are applied.
// Call from Load (or explicitly after constructing a Config) for fail-fast startup.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}

	if strings.TrimSpace(c.Server.Host) == "" {
		return fmt.Errorf("server.host is required")
	}
	if strings.TrimSpace(c.Server.Port) == "" {
		return fmt.Errorf("server.port is required")
	}
	if port, err := strconv.Atoi(strings.TrimSpace(c.Server.Port)); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("server.port must be a TCP port number 1-65535, got %q", c.Server.Port)
	}
	if c.Server.LatestQuoteCacheTTLSeconds < 0 {
		return fmt.Errorf("server.latest_quote_cache_ttl_seconds must be >= 0, got %d", c.Server.LatestQuoteCacheTTLSeconds)
	}
	if c.Server.ReadTimeout <= 0 {
		return fmt.Errorf("server.read_timeout must be > 0, got %s", time.Duration(c.Server.ReadTimeout))
	}
	if c.Server.WriteTimeout <= 0 {
		return fmt.Errorf("server.write_timeout must be > 0, got %s", time.Duration(c.Server.WriteTimeout))
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("server.read_header_timeout must be > 0, got %s", time.Duration(c.Server.ReadHeaderTimeout))
	}
	if c.Server.IdleTimeout <= 0 {
		return fmt.Errorf("server.idle_timeout must be > 0, got %s", time.Duration(c.Server.IdleTimeout))
	}
	if gm := strings.ToLower(strings.TrimSpace(c.Server.GinMode)); gm != "" {
		switch gm {
		case "debug", "release", "test":
		default:
			return fmt.Errorf("server.gin_mode must be one of debug, release, test, or empty, got %q", c.Server.GinMode)
		}
	}
	if len(c.Server.CORS.AllowedOrigins) == 0 {
		return fmt.Errorf("server.cors.allowed_origins must contain at least one origin")
	}
	for _, o := range c.Server.CORS.AllowedOrigins {
		o = strings.TrimSpace(o)
		if o == "" {
			return fmt.Errorf("server.cors.allowed_origins: empty entry is not allowed")
		}
		if o == "*" {
			return fmt.Errorf("server.cors.allowed_origins: wildcard '*' is not allowed; list explicit http(s) origins")
		}
		if !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
			return fmt.Errorf("server.cors.allowed_origins: origin %q must start with http:// or https://", o)
		}
	}

	if strings.TrimSpace(c.Database.Host) == "" {
		return fmt.Errorf("database.host is required")
	}
	if strings.TrimSpace(c.Database.Port) == "" {
		return fmt.Errorf("database.port is required")
	}
	if dbPort, err := strconv.Atoi(strings.TrimSpace(c.Database.Port)); err != nil || dbPort < 1 || dbPort > 65535 {
		return fmt.Errorf("database.port must be a TCP port number 1-65535, got %q", c.Database.Port)
	}
	if strings.TrimSpace(c.Database.User) == "" {
		return fmt.Errorf("database.user is required")
	}
	if strings.TrimSpace(c.Database.Name) == "" {
		return fmt.Errorf("database.name is required")
	}

	if c.Job.Enabled && c.Job.IntervalSeconds <= 0 {
		return fmt.Errorf("job.interval_seconds must be > 0 when job.enabled is true")
	}

	if c.API.TimeoutSeconds <= 0 {
		return fmt.Errorf("api.timeout_seconds must be > 0")
	}

	if strings.TrimSpace(c.CoinGecko.BaseURL) == "" {
		return fmt.Errorf("coingecko.base_url is required")
	}
	if err := validateRateLimit("coingecko", c.CoinGecko.RateLimit); err != nil {
		return err
	}
	if err := validateRateLimit("equiteez", c.Equiteez.RateLimit); err != nil {
		return err
	}

	if c.Backfill.TickSeconds < 0 {
		return fmt.Errorf("backfill.tick_seconds must be >= 0")
	}
	if c.Backfill.ChunkMinutes < 0 {
		return fmt.Errorf("backfill.chunk_minutes must be >= 0")
	}
	// CoinGecko market_chart/range granularity drops to 1h once the window exceeds
	// ~1 day; we keep chunks <= 24h to preserve 5-min points for the backfill pass.
	if c.Backfill.ChunkMinutes > 1440 {
		return fmt.Errorf("backfill.chunk_minutes must be <= 1440 (24h) to keep CoinGecko 5-min granularity, got %d", c.Backfill.ChunkMinutes)
	}
	if c.Backfill.BackfillMaxErrors < 0 {
		return fmt.Errorf("backfill.backfill_max_errors must be >= 0")
	}
	if c.Backfill.BackoffInitialMs < 0 {
		return fmt.Errorf("backfill.backoff_initial_ms must be >= 0")
	}
	if c.Backfill.BackoffMaxMs < 0 {
		return fmt.Errorf("backfill.backoff_max_ms must be >= 0")
	}
	if err := validateBackfillStartFrom(c.Backfill.StartFrom); err != nil {
		return fmt.Errorf("backfill.start_from: %w", err)
	}
	if err := validateBackfillStartFrom(c.Backfill.MinStartFrom); err != nil {
		return fmt.Errorf("backfill.min_start_from: %w", err)
	}

	for name := range c.Tokens {
		key := strings.ToLower(strings.TrimSpace(name))
		if !quotes.IsTokenSupported(key) {
			return fmt.Errorf("tokens: unknown or unsupported token key %q (supported: %v)", name, quotes.GetSupportedTokenNames())
		}
		tc := c.Tokens[name]
		if err := validateBackfillStartFrom(tc.Backfill.StartFrom); err != nil {
			return fmt.Errorf("tokens[%s].backfill.start_from: %w", name, err)
		}
		if tc.Backfill.ChunkMinutes < 0 {
			return fmt.Errorf("tokens[%s].backfill.chunk_minutes must be >= 0", name)
		}
		if tc.Backfill.ChunkMinutes > 1440 {
			return fmt.Errorf("tokens[%s].backfill.chunk_minutes must be <= 1440 (24h), got %d", name, tc.Backfill.ChunkMinutes)
		}
		if tc.LiveLookbackSeconds < 0 {
			return fmt.Errorf("tokens[%s].live_lookback_seconds must be >= 0", name)
		}
	}

	for _, tok := range quotes.GetSupportedTokens() {
		tokenName := string(tok)
		if !c.IsTokenBackfillEnabled(tokenName) {
			continue
		}
		start := strings.TrimSpace(c.GetTokenBackfillStartFrom(tokenName))
		if start == "" {
			return fmt.Errorf("backfill is enabled for token %q but start_from is empty (set globally or under tokens.%s.backfill)", tokenName, tokenName)
		}
		if err := validateBackfillStartFrom(start); err != nil {
			return fmt.Errorf("resolved backfill start_from for token %q: %w", tokenName, err)
		}
	}

	if c.Job.Enabled {
		for _, tok := range quotes.GetSupportedTokens() {
			tokenName := string(tok)
			if !c.IsTokenEnabled(tokenName) {
				continue
			}
			if c.GetTokenInterval(tokenName) <= 0 {
				return fmt.Errorf("tokens[%s]: interval must resolve to > 0 when job is enabled", tokenName)
			}
		}
	}

	return nil
}

func validateRateLimit(section string, r RateLimitConfig) error {
	if r.RPS < 0 {
		return fmt.Errorf("%s.rate_limit.rps must be >= 0, got %v", section, r.RPS)
	}
	if r.Burst < 0 {
		return fmt.Errorf("%s.rate_limit.burst must be >= 0, got %d", section, r.Burst)
	}
	return nil
}

func validateBackfillStartFrom(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return nil
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return nil
	}
	return fmt.Errorf("must be RFC3339 (e.g. 2025-01-15T00:00:00Z) or date YYYY-MM-DD, got %q", s)
}
