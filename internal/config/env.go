package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

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
	if rateLimit := os.Getenv("API_RATE_LIMIT_RPS"); rateLimit != "" {
		if val, err := strconv.Atoi(rateLimit); err == nil {
			config.API.RateLimitRPS = val
		}
	}
	if burst := os.Getenv("API_RATE_LIMIT_BURST"); burst != "" {
		if val, err := strconv.Atoi(burst); err == nil {
			config.API.RateLimitBurst = val
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

	if enabled := os.Getenv("BACKFILL_ENABLED"); enabled != "" {
		if val, err := strconv.ParseBool(enabled); err == nil {
			config.Backfill.Enabled = val
		}
	}
	if startFrom := os.Getenv("BACKFILL_START_FROM"); startFrom != "" {
		config.Backfill.StartFrom = startFrom
	}
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
	return nil
}
