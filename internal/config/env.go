package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

//nolint:gocyclo // Many independent env key bindings; one place keeps wiring obvious.
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
	if v := os.Getenv("SERVER_PPROF_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SERVER_PPROF_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Server.PprofEnabled = val
	}
	if v := os.Getenv("SERVER_MAX_QUERY_LIMIT"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SERVER_MAX_QUERY_LIMIT: invalid int %q: %w", v, err)
		}
		config.Server.MaxQueryLimit = val
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
	if v := os.Getenv("POSTGRES_LOGGING"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("POSTGRES_LOGGING: invalid bool %q: %w", v, err)
		}
		config.Database.Logging = val
	}

	if v := os.Getenv("JOB_INTERVAL_SECONDS"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("JOB_INTERVAL_SECONDS: invalid int %q: %w", v, err)
		}
		config.Job.IntervalSeconds = val
	}
	if v := os.Getenv("JOB_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("JOB_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Job.Enabled = val
	}

	if v := os.Getenv("API_TIMEOUT_SECONDS"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("API_TIMEOUT_SECONDS: invalid int %q: %w", v, err)
		}
		config.API.TimeoutSeconds = val
	}
	for _, e := range []struct {
		env string
		dst *int
	}{
		{"OUTBOUND_HTTP_RETRY_MAX_ATTEMPTS", &config.API.OutboundHTTPRetryMaxAttempts},
		{"OUTBOUND_HTTP_RETRY_INITIAL_MS", &config.API.OutboundHTTPRetryInitialMS},
		{"OUTBOUND_HTTP_RETRY_MAX_MS", &config.API.OutboundHTTPRetryMaxMS},
		{"OUTBOUND_HTTP_CIRCUIT_BREAKER_INTERVAL_SECONDS", &config.API.OutboundHTTPCircuitBreakerIntervalSeconds},
		{"OUTBOUND_HTTP_CIRCUIT_BREAKER_OPEN_SECONDS", &config.API.OutboundHTTPCircuitBreakerOpenSeconds},
	} {
		if v := os.Getenv(e.env); v != "" {
			val, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: invalid int %q: %w", e.env, v, err)
			}
			*e.dst = val
		}
	}
	if v := os.Getenv("OUTBOUND_HTTP_CIRCUIT_BREAKER_DISABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("OUTBOUND_HTTP_CIRCUIT_BREAKER_DISABLED: invalid bool %q: %w", v, err)
		}
		config.API.OutboundHTTPCircuitBreakerDisabled = val
	}
	for _, e := range []struct {
		env string
		dst *uint32
	}{
		{"OUTBOUND_HTTP_CIRCUIT_BREAKER_HALF_OPEN_MAX_REQUESTS", &config.API.OutboundHTTPCircuitBreakerHalfOpenMaxRequests},
		{"OUTBOUND_HTTP_CIRCUIT_BREAKER_TRIP_AFTER_FAILURES", &config.API.OutboundHTTPCircuitBreakerTripAfterFailures},
	} {
		if v := os.Getenv(e.env); v != "" {
			val, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return fmt.Errorf("%s: invalid uint %q: %w", e.env, v, err)
			}
			*e.dst = uint32(val)
		}
	}

	if apiKey := os.Getenv("COINGECKO_API_KEY"); apiKey != "" {
		config.CoinGecko.APIKey = apiKey
	}
	if baseURL := os.Getenv("COINGECKO_BASE_URL"); baseURL != "" {
		config.CoinGecko.BaseURL = baseURL
	}
	if v := os.Getenv("COINGECKO_RATE_LIMIT_RPS"); v != "" {
		val, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("COINGECKO_RATE_LIMIT_RPS: invalid float %q: %w", v, err)
		}
		config.CoinGecko.RateLimit.RPS = val
	}
	if v := os.Getenv("COINGECKO_RATE_LIMIT_BURST"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("COINGECKO_RATE_LIMIT_BURST: invalid int %q: %w", v, err)
		}
		config.CoinGecko.RateLimit.Burst = val
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
	if v := os.Getenv("EQUITEEZ_RATE_LIMIT_RPS"); v != "" {
		val, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("EQUITEEZ_RATE_LIMIT_RPS: invalid float %q: %w", v, err)
		}
		config.Equiteez.RateLimit.RPS = val
	}
	if v := os.Getenv("EQUITEEZ_RATE_LIMIT_BURST"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("EQUITEEZ_RATE_LIMIT_BURST: invalid int %q: %w", v, err)
		}
		config.Equiteez.RateLimit.Burst = val
	}

	if v := os.Getenv("BACKFILL_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("BACKFILL_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Backfill.Enabled = val
	}
	if v := os.Getenv("BACKFILL_START_FROM"); v != "" {
		config.Backfill.StartFrom = v
	}
	if v := os.Getenv("BACKFILL_MIN_START_FROM"); v != "" {
		config.Backfill.MinStartFrom = v
	}
	for _, e := range []struct {
		env string
		dst *int
	}{
		{"BACKFILL_TICK_SECONDS", &config.Backfill.TickSeconds},
		{"BACKFILL_JITTER_MS", &config.Backfill.JitterMs},
		{"BACKFILL_CHUNK_MINUTES", &config.Backfill.ChunkMinutes},
		{"BACKFILL_MAX_ERRORS", &config.Backfill.BackfillMaxErrors},
		{"BACKFILL_BACKOFF_INITIAL_MS", &config.Backfill.BackoffInitialMs},
		{"BACKFILL_BACKOFF_MAX_MS", &config.Backfill.BackoffMaxMs},
		{"BACKFILL_MAX_BACKOFF_MS", &config.Backfill.MaxBackoffMs},
	} {
		if v := os.Getenv(e.env); v != "" {
			val, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: invalid int %q: %w", e.env, v, err)
			}
			*e.dst = val
		}
	}

	if v := os.Getenv("EQUITEEZ_BACKFILL_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("EQUITEEZ_BACKFILL_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Equiteez.Backfill.Enabled = val
	}
	if v := os.Getenv("EQUITEEZ_BACKFILL_START_FROM"); v != "" {
		config.Equiteez.Backfill.StartFrom = v
	}
	for _, e := range []struct {
		env string
		dst *int
	}{
		{"EQUITEEZ_BACKFILL_TICK_SECONDS", &config.Equiteez.Backfill.TickSeconds},
		{"EQUITEEZ_BACKFILL_BATCH_SIZE", &config.Equiteez.Backfill.BatchSize},
		{"EQUITEEZ_BACKFILL_JITTER_MS", &config.Equiteez.Backfill.JitterMs},
		{"EQUITEEZ_BACKFILL_MAX_ERRORS", &config.Equiteez.Backfill.BackfillMaxErrors},
		{"EQUITEEZ_BACKFILL_BACKOFF_INITIAL_MS", &config.Equiteez.Backfill.BackoffInitialMs},
		{"EQUITEEZ_BACKFILL_BACKOFF_MAX_MS", &config.Equiteez.Backfill.BackoffMaxMs},
		{"EQUITEEZ_BACKFILL_MAX_BACKOFF_MS", &config.Equiteez.Backfill.MaxBackoffMs},
	} {
		if v := os.Getenv(e.env); v != "" {
			val, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: invalid int %q: %w", e.env, v, err)
			}
			*e.dst = val
		}
	}

	if v := os.Getenv("RWA_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("RWA_ENABLED: invalid bool %q: %w", v, err)
		}
		config.RWA.Enabled = val
	}
	if v := os.Getenv("RWA_INTERVAL_SECONDS"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("RWA_INTERVAL_SECONDS: invalid int %q: %w", v, err)
		}
		config.RWA.IntervalSeconds = val
	}
	if v := os.Getenv("RWA_CONCURRENCY"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("RWA_CONCURRENCY: invalid int %q: %w", v, err)
		}
		config.RWA.Concurrency = val
	}
	return nil
}
