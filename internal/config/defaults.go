package config

import (
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
		// Secure-by-default: "prefer" negotiates TLS when the server offers it and
		// transparently falls back to plaintext for a local/compose Postgres that
		// has none — so a managed/remote DB is encrypted without an explicit opt-in,
		// while local dev keeps working. Set POSTGRES_SSL=require to enforce.
		config.Database.SSLMode = "prefer"
	}

	if config.Job.IntervalSeconds == 0 {
		config.Job.IntervalSeconds = 60
	}

	if config.API.TimeoutSeconds == 0 {
		config.API.TimeoutSeconds = 30
	}
	if config.API.OutboundMaxResponseBytes == 0 {
		// Cap outbound response bodies at 16 MiB by default so a misbehaving or
		// compromised upstream (or a huge Cloudflare error page) can't stream
		// gigabytes into memory via graphql.Execute's io.ReadAll. A negative value
		// is treated as an explicit "disabled" escape hatch (MaxBytesReader).
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
		// 10s matches Mavryk block time — orderbook prices on Equiteez change
		// at most once per block, so polling faster doesn't surface fresher
		// data. Operators can lower further on chains with sub-second blocks
		// (rare) or raise to reduce storage pressure on idle markets.
		config.RWA.IntervalSeconds = 10
	}
	if config.RWA.Concurrency == 0 && config.RWA.Enabled {
		config.RWA.Concurrency = 4
	}

	// Tickers defaults — only the cache TTLs are eager; the rest stay zero
	// unless explicitly enabled in YAML so a fresh service doesn't start
	// hammering CG without an operator decision.
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
