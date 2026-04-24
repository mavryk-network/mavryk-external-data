package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

//nolint:gocyclo // Many independent env key bindings; a single function keeps config wiring in one file.
func overrideWithEnv(config *Config) error {
	if port := os.Getenv("SERVER_PORT"); port != "" {
		config.Server.Port = port
	}
	if host := os.Getenv("SERVER_HOST"); host != "" {
		config.Server.Host = host
	}
	if v := os.Getenv("SERVER_GIN_MODE"); v != "" {
		config.Server.GinMode = v
	}
	for _, e := range []struct {
		env string
		dst *DurationYAML
	}{
		{"SERVER_READ_TIMEOUT", &config.Server.ReadTimeout},
		{"SERVER_WRITE_TIMEOUT", &config.Server.WriteTimeout},
		{"SERVER_READ_HEADER_TIMEOUT", &config.Server.ReadHeaderTimeout},
		{"SERVER_IDLE_TIMEOUT", &config.Server.IdleTimeout},
	} {
		if v := os.Getenv(e.env); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: invalid duration %q: %w", e.env, v, err)
			}
			*e.dst = DurationYAML(d)
		}
	}
	if v := os.Getenv("SERVER_CORS_ALLOWED_ORIGINS"); v != "" {
		parts := strings.Split(v, ",")
		var origins []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				origins = append(origins, p)
			}
		}
		if len(origins) > 0 {
			config.Server.CORS.AllowedOrigins = origins
		}
	}

	if host := os.Getenv("POSTGRES_HOST"); host != "" {
		config.Database.Host = host
	}
	if port := os.Getenv("POSTGRES_PORT"); port != "" {
		config.Database.Port = port
	}
	if user := os.Getenv("POSTGRES_USER"); user != "" {
		config.Database.User = user
	}
	if password := os.Getenv("POSTGRES_PASSWORD"); password != "" {
		config.Database.Password = password
	}
	if name := os.Getenv("POSTGRES_DATABASE"); name != "" {
		config.Database.Name = name
	}
	if sslMode := os.Getenv("POSTGRES_SSL"); sslMode != "" {
		config.Database.SSLMode = sslMode
	}

	if logging := os.Getenv("POSTGRES_LOGGING"); logging != "" {
		if val, err := strconv.ParseBool(logging); err == nil {
			config.Database.Logging = val
		}
	}

	if interval := os.Getenv("JOB_INTERVAL_SECONDS"); interval != "" {
		if val, err := strconv.Atoi(interval); err == nil {
			config.Job.IntervalSeconds = val
		}
	}
	if enabled := os.Getenv("JOB_ENABLED"); enabled != "" {
		if val, err := strconv.ParseBool(enabled); err == nil {
			config.Job.Enabled = val
		}
	}

	if timeout := os.Getenv("API_TIMEOUT_SECONDS"); timeout != "" {
		if val, err := strconv.Atoi(timeout); err == nil {
			config.API.TimeoutSeconds = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_RETRY_MAX_ATTEMPTS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.API.OutboundHTTPRetryMaxAttempts = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_RETRY_INITIAL_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.API.OutboundHTTPRetryInitialMS = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_RETRY_MAX_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.API.OutboundHTTPRetryMaxMS = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_DISABLED"); v != "" {
		if val, err := strconv.ParseBool(v); err == nil {
			config.API.OutboundHTTPCircuitBreakerDisabled = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_INTERVAL_SECONDS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.API.OutboundHTTPCircuitBreakerIntervalSeconds = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_OPEN_SECONDS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.API.OutboundHTTPCircuitBreakerOpenSeconds = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_HALF_OPEN_MAX_REQUESTS"); v != "" {
		if val, err := strconv.ParseUint(v, 10, 32); err == nil {
			config.API.OutboundHTTPCircuitBreakerHalfOpenMaxRequests = uint32(val)
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_TRIP_AFTER_FAILURES"); v != "" {
		if val, err := strconv.ParseUint(v, 10, 32); err == nil {
			config.API.OutboundHTTPCircuitBreakerTripAfterFailures = uint32(val)
		}
	}

	if apiKey := os.Getenv("COINGECKO_API_KEY"); apiKey != "" {
		config.CoinGecko.APIKey = apiKey
	}
	if baseURL := os.Getenv("COINGECKO_BASE_URL"); baseURL != "" {
		config.CoinGecko.BaseURL = baseURL
	}
	// Per-service outbound throttle (shared registry key "coingecko"). Float
	// accepted (e.g. 0.5 = one req / 2s). An explicit post-defaults 0 disables
	// the limiter — that escape hatch lives in load.go.
	if v := os.Getenv("COINGECKO_RATE_LIMIT_RPS"); v != "" {
		if val, err := strconv.ParseFloat(v, 64); err == nil {
			config.CoinGecko.RateLimit.RPS = val
		}
	}
	if v := os.Getenv("COINGECKO_RATE_LIMIT_BURST"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.CoinGecko.RateLimit.Burst = val
		}
	}

	if v := os.Getenv("EQUITEEZ_INDEXER_URL"); v != "" {
		config.Equiteez.IndexerURL = v
	}
	if v := os.Getenv("EQUITEEZ_INDEXER_PASSWORD"); v != "" {
		config.Equiteez.IndexerPassword = v
	}
	if v := os.Getenv("EQUITEEZ_TOKEN_INDEXER_URL"); v != "" {
		config.Equiteez.TokenIndexerURL = v
	}
	if v := os.Getenv("EQUITEEZ_TOKEN_INDEXER_PASSWORD"); v != "" {
		config.Equiteez.TokenIndexerPassword = v
	}
	// Equiteez outbound throttle (shared registry key "equiteez"). Default 0
	// (disabled); Hasura admin-secret endpoints usually have no per-IP quota.
	if v := os.Getenv("EQUITEEZ_RATE_LIMIT_RPS"); v != "" {
		if val, err := strconv.ParseFloat(v, 64); err == nil {
			config.Equiteez.RateLimit.RPS = val
		}
	}
	if v := os.Getenv("EQUITEEZ_RATE_LIMIT_BURST"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Equiteez.RateLimit.Burst = val
		}
	}

	if enabled := os.Getenv("BACKFILL_ENABLED"); enabled != "" {
		if val, err := strconv.ParseBool(enabled); err == nil {
			config.Backfill.Enabled = val
		}
	}
	if startFrom := os.Getenv("BACKFILL_START_FROM"); startFrom != "" {
		config.Backfill.StartFrom = startFrom
	}
	if v := os.Getenv("BACKFILL_MIN_START_FROM"); v != "" {
		config.Backfill.MinStartFrom = v
	}
	if v := os.Getenv("BACKFILL_TICK_SECONDS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Backfill.TickSeconds = val
		}
	}
	// BACKFILL_SLEEP_MS is deprecated: cadence is set by BACKFILL_TICK_SECONDS. We still
	// parse the value so old .env files do not break; it is ignored at runtime.
	if sleep := os.Getenv("BACKFILL_SLEEP_MS"); sleep != "" {
		if val, err := strconv.Atoi(sleep); err == nil {
			config.Backfill.SleepMs = val
		}
	}
	if chunk := os.Getenv("BACKFILL_CHUNK_MINUTES"); chunk != "" {
		if val, err := strconv.Atoi(chunk); err == nil {
			config.Backfill.ChunkMinutes = val
		}
	}
	if v := os.Getenv("BACKFILL_MAX_ERRORS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Backfill.BackfillMaxErrors = val
		}
	}
	if v := os.Getenv("BACKFILL_BACKOFF_INITIAL_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Backfill.BackoffInitialMs = val
		}
	}
	if v := os.Getenv("BACKFILL_BACKOFF_MAX_MS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Backfill.BackoffMaxMs = val
		}
	}
	return nil
}
