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
		config.Server.LatestQuoteCacheTTLSeconds = 5
	}
	if config.Server.MaxQueryLimit == 0 {
		config.Server.MaxQueryLimit = 10000
	}
	if config.Server.FXMaxStalenessSeconds == 0 {
		config.Server.FXMaxStalenessSeconds = 300
	}
	if config.Server.MaxInCurrencies == 0 {
		config.Server.MaxInCurrencies = 10
	}
	if len(config.Server.CORS.AllowedOrigins) == 0 {
		config.Server.CORS.AllowedOrigins = []string{
			"http://localhost:3010",
			"http://127.0.0.1:3010",
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
		config.Job.IntervalSeconds = 60
	}

	if config.API.TimeoutSeconds == 0 {
		config.API.TimeoutSeconds = 30
	}

	if config.CoinGecko.BaseURL == "" {
		config.CoinGecko.BaseURL = "https://api.coingecko.com/api/v3"
	}
	if config.CoinGecko.RateLimit.RPS == 0 {
		config.CoinGecko.RateLimit.RPS = 8
	}

	if config.Backfill.TickSeconds == 0 {
		config.Backfill.TickSeconds = 5
	}
	if config.Backfill.ChunkMinutes == 0 {
		config.Backfill.ChunkMinutes = 360
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
	if config.Backfill.MaxBackoffMs == 0 {
		// 24h hard cap so a stuck token doesn't stretch backoff to weeks.
		config.Backfill.MaxBackoffMs = 24 * 60 * 60 * 1000
	}

	if config.Equiteez.Backfill.TickSeconds == 0 {
		config.Equiteez.Backfill.TickSeconds = 30
	}
	if config.Equiteez.Backfill.BatchSize == 0 {
		config.Equiteez.Backfill.BatchSize = 200
	}
	if config.Equiteez.Backfill.JitterMs == 0 {
		config.Equiteez.Backfill.JitterMs = 1000
	}
	if config.Equiteez.Backfill.BackfillMaxErrors == 0 {
		config.Equiteez.Backfill.BackfillMaxErrors = 5
	}
	if config.Equiteez.Backfill.BackoffInitialMs == 0 {
		config.Equiteez.Backfill.BackoffInitialMs = 2000
	}
	if config.Equiteez.Backfill.BackoffMaxMs == 0 {
		config.Equiteez.Backfill.BackoffMaxMs = 60_000
	}
	if config.Equiteez.Backfill.MaxBackoffMs == 0 {
		config.Equiteez.Backfill.MaxBackoffMs = 24 * 60 * 60 * 1000
	}

	if config.RWA.IntervalSeconds == 0 && config.RWA.Enabled {
		// 10s matches Mavryk block time — orderbook prices on Equiteez change
		// at most once per block, so polling faster doesn't surface fresher
		// data. Operators can lower further on chains with sub-second blocks
		// (rare) or raise to reduce storage pressure on idle markets.
		config.RWA.IntervalSeconds = 10
	}
	if config.RWA.Concurrency == 0 && config.RWA.Enabled {
		config.RWA.Concurrency = 4
	}
}
