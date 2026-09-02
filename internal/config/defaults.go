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
	// Normalize known values only; an unknown one is left for validateGinMode to
	// reject with an accurate message.
	switch strings.ToLower(strings.TrimSpace(config.Server.GinMode)) {
	case "", "debug", "release", "test":
		config.Server.GinMode = config.Server.EffectiveGinMode()
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
	if config.Server.HandlerTimeout == 0 {
		// Absent key must not mean "no per-request budget"; negative is the
		// explicit escape hatch.
		config.Server.HandlerTimeout = DurationYAML(10 * time.Second)
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
	if config.Server.TickerStaleAfter == 0 {
		config.Server.TickerStaleAfter = DurationYAML(60 * time.Minute)
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
		// "prefer" negotiates TLS when offered and falls back to plaintext for a
		// local Postgres without it. POSTGRES_SSL=require to enforce.
		config.Database.SSLMode = "prefer"
	}

	if config.Job.IntervalSeconds == 0 {
		config.Job.IntervalSeconds = 60
	}

	if config.API.TimeoutSeconds == 0 {
		config.API.TimeoutSeconds = 30
	}
	if config.API.OutboundMaxResponseBytes == 0 {
		// 16 MiB so a misbehaving upstream can't stream gigabytes into memory via
		// graphql.Execute's io.ReadAll. Negative disables the cap.
		config.API.OutboundMaxResponseBytes = 16 << 20
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

	if config.Auth.JWKSCacheTTL == 0 {
		config.Auth.JWKSCacheTTL = defaultAuthConfig().JWKSCacheTTL
	}

	if config.RWA.IntervalSeconds == 0 && config.RWA.Enabled {
		// 10s = Mavryk block time; orderbook prices change at most once per block.
		config.RWA.IntervalSeconds = 10
	}
	if config.RWA.PairSyncIntervalSeconds == 0 && config.RWA.Enabled {
		// Hourly: listings are rare and one GraphQL query per hour is free.
		config.RWA.PairSyncIntervalSeconds = 3600
	}

	if config.Tickers.TokenSymbol == "" {
		config.Tickers.TokenSymbol = "mvrk"
	}
	if config.Tickers.IntervalSeconds == 0 {
		config.Tickers.IntervalSeconds = 300 // 5 min
	}
	if config.Tickers.HTTPTimeout == 0 {
		config.Tickers.HTTPTimeout = DurationYAML(10 * time.Second)
	}
	if config.Tickers.Cache.LatestTTLSeconds == 0 {
		config.Tickers.Cache.LatestTTLSeconds = 30
	}
	if config.Tickers.Cache.DistributionTTLSeconds == 0 {
		config.Tickers.Cache.DistributionTTLSeconds = 60
	}
}
