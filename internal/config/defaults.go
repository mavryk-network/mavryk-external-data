package config

import (
	"strings"
	"time"
)

func setDefaults(config *Config) {
	if config.Server.Port == "" {
		config.Server.Port = "3010"
	}
	if config.Server.Host == "" {
		config.Server.Host = "0.0.0.0"
	}
	if config.Server.ReadTimeout == 0 {
		config.Server.ReadTimeout = DurationYAML(30 * time.Second)
	}
	if config.Server.WriteTimeout == 0 {
		config.Server.WriteTimeout = DurationYAML(30 * time.Second)
	}
	if config.Server.ReadHeaderTimeout == 0 {
		config.Server.ReadHeaderTimeout = DurationYAML(10 * time.Second)
	}
	if config.Server.IdleTimeout == 0 {
		config.Server.IdleTimeout = DurationYAML(120 * time.Second)
	}
	if config.Server.LatestQuoteCacheTTLSeconds == 0 {
		config.Server.LatestQuoteCacheTTLSeconds = 5 // seconds; disable with SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS=0 after defaults
	}
	if len(config.Server.CORS.AllowedOrigins) == 0 {
		config.Server.CORS.AllowedOrigins = []string{
			"http://localhost:3000",
			"http://127.0.0.1:3000",
			"http://localhost:8080",
			"http://127.0.0.1:8080",
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		}
	}
	if strings.TrimSpace(config.Server.CORS.AllowedMethods) == "" {
		config.Server.CORS.AllowedMethods = "GET, HEAD, OPTIONS"
	}
	if strings.TrimSpace(config.Server.CORS.AllowedHeaders) == "" {
		config.Server.CORS.AllowedHeaders = "Origin, Content-Type, Accept"
	}

	if config.Database.Host == "" {
		config.Database.Host = "localhost"
	}
	if config.Database.Port == "" {
		config.Database.Port = "5432"
	}
	if config.Database.User == "" {
		config.Database.User = "postgres"
	}
	if config.Database.Password == "" {
		config.Database.Password = "postgres"
	}
	if config.Database.Name == "" {
		config.Database.Name = "quotes"
	}
	if config.Database.SSLMode == "" {
		config.Database.SSLMode = "disable"
	}

	if config.Job.IntervalSeconds == 0 {
		config.Job.IntervalSeconds = 60 // 1 minute
	}

	if config.API.TimeoutSeconds == 0 {
		config.API.TimeoutSeconds = 30
	}

	if config.CoinGecko.BaseURL == "" {
		config.CoinGecko.BaseURL = "https://api.coingecko.com/api/v3"
	}
	// Safe outbound default for CoinGecko Pro Analyst tier (500 req/min ≈ 8.33 rps).
	// A missing value should never mean "unlimited" — override via env to tune up
	// (Pro = 60, Pro Lite = 16) or set COINGECKO_RATE_LIMIT_RPS=0 to disable.
	if config.CoinGecko.RateLimit.RPS == 0 {
		config.CoinGecko.RateLimit.RPS = 8
	}
	// Equiteez indexer is private Hasura — leave RateLimit at 0 (disabled) by
	// default. Consumers can opt in per deployment.

	// Backfill defaults
	// Disabled by default; explicit opt-in.
	// StartFrom default left empty (will be treated as no-op).
	if config.Backfill.TickSeconds == 0 {
		config.Backfill.TickSeconds = 5 // single reverse step every 5s per token, globally
	}
	if config.Backfill.ChunkMinutes == 0 {
		config.Backfill.ChunkMinutes = 360 // 6h per step → stays inside CoinGecko 5-min granularity window
	}
	if config.Backfill.BackfillMaxErrors == 0 {
		config.Backfill.BackfillMaxErrors = 5
	}
	if config.Backfill.BackoffInitialMs == 0 {
		config.Backfill.BackoffInitialMs = 2000
	}
	if config.Backfill.BackoffMaxMs == 0 {
		config.Backfill.BackoffMaxMs = 60_000
	}
	// config.Backfill.SleepMs is deprecated and intentionally left untouched.
}
