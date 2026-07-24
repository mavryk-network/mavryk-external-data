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
	if port := os.Getenv("SERVER_INTERNAL_PORT"); port != "" {
		config.Server.InternalPort = port
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
	if v := os.Getenv("SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS"); v != "" {
		val, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS: invalid int %q: %w", v, err)
		}
		config.Server.LatestQuoteCacheTTLSeconds = val
	}
	for _, e := range []struct {
		env string
		dst *DurationYAML
	}{
		{"SERVER_READ_TIMEOUT", &config.Server.ReadTimeout},
		{"SERVER_WRITE_TIMEOUT", &config.Server.WriteTimeout},
		{"SERVER_READ_HEADER_TIMEOUT", &config.Server.ReadHeaderTimeout},
		{"SERVER_IDLE_TIMEOUT", &config.Server.IdleTimeout},
		{"SERVER_HANDLER_TIMEOUT", &config.Server.HandlerTimeout},
		{"SERVER_TICKER_STALE_AFTER", &config.Server.TickerStaleAfter},
	} {
		if v := os.Getenv(e.env); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: invalid duration %q: %w", e.env, v, err)
			}
			*e.dst = DurationYAML(d)
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
	if v := os.Getenv("OUTBOUND_MAX_RESPONSE_BYTES"); v != "" {
		val, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("OUTBOUND_MAX_RESPONSE_BYTES: invalid int %q: %w", v, err)
		}
		config.API.OutboundMaxResponseBytes = val
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

	if v := os.Getenv("TICKERS_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TICKERS_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Tickers.Enabled = val
	}
	if v := os.Getenv("TICKERS_TOKEN_SYMBOL"); v != "" {
		config.Tickers.TokenSymbol = v
	}
	if v := os.Getenv("TICKERS_INCLUDE_EXCHANGE_LOGO"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TICKERS_INCLUDE_EXCHANGE_LOGO: invalid bool %q: %w", v, err)
		}
		config.Tickers.IncludeExchangeLogo = val
	}
	for _, e := range []struct {
		env string
		dst *int
	}{
		{"TICKERS_INTERVAL_SECONDS", &config.Tickers.IntervalSeconds},
		{"TICKERS_CACHE_LATEST_TTL_SECONDS", &config.Tickers.Cache.LatestTTLSeconds},
		{"TICKERS_CACHE_DISTRIBUTION_TTL_SECONDS", &config.Tickers.Cache.DistributionTTLSeconds},
	} {
		if v := os.Getenv(e.env); v != "" {
			val, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: invalid int %q: %w", e.env, v, err)
			}
			*e.dst = val
		}
	}
	if v := os.Getenv("TICKERS_HTTP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("TICKERS_HTTP_TIMEOUT: invalid duration %q: %w", v, err)
		}
		config.Tickers.HTTPTimeout = DurationYAML(d)
	}

	if v := os.Getenv("AUTH_ENABLED"); v != "" {
		val, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("AUTH_ENABLED: invalid bool %q: %w", v, err)
		}
		config.Auth.Enabled = &val
	}
	if v := os.Getenv("AUTH_MBIO_API_GATEWAY_BASE_URL"); v != "" {
		config.Auth.MBIOAPIGatewayBaseURL = v
	}
	if v := os.Getenv("AUTH_MBIO_JWT_BASE_URL"); v != "" {
		config.Auth.MBIOJWTBaseURL = v
	}
	if v := os.Getenv("AUTH_MBIO_JWT_ISSUER"); v != "" {
		config.Auth.MBIOJWTIssuer = v
	}
	if v := os.Getenv("AUTH_MBIO_JWT_AUDIENCE"); v != "" {
		config.Auth.MBIOJWTAudience = v
	}
	if v := os.Getenv("AUTH_JWKS_CACHE_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("AUTH_JWKS_CACHE_TTL: invalid duration %q: %w", v, err)
		}
		config.Auth.JWKSCacheTTL = d
	}
	if v := os.Getenv("AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY"); v != "" {
		config.Auth.JWTLocalVerifyPublicKeyBase64 = v
	}

	return applyTokenEnvOverrides(config)
}

// applyTokenEnvOverrides scans TOKEN_<NAME>__<FIELD> env vars and merges them
// into config.Tokens. Double underscore separates the token name from the
// field name so tokens with underscores in the name (wrapped_btc) parse
// unambiguously. Tokens absent from yaml get Enabled=true by default —
// matches GetTokenConfig's "unknown token = enabled" semantics, so a one-off
// `TOKEN_FOO__INTERVAL_SECONDS=60` doesn't silently land disabled.
func applyTokenEnvOverrides(config *Config) error {
	const prefix = "TOKEN_"
	const sep = "__"

	if config.Tokens == nil {
		config.Tokens = make(map[string]TokenConfig)
	}

	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(key, prefix) || val == "" {
			continue
		}
		rest := key[len(prefix):]
		sepIdx := strings.Index(rest, sep)
		if sepIdx <= 0 {
			continue
		}
		tokenName := strings.ToLower(rest[:sepIdx])
		field := rest[sepIdx+len(sep):]
		if tokenName == "" || field == "" {
			return fmt.Errorf("%s: malformed env var (expected TOKEN_<name>%s<field>)", key, sep)
		}

		tc, exists := config.Tokens[tokenName]
		if !exists {
			tc = TokenConfig{Enabled: true}
		}
		if err := applyTokenField(&tc, field, val); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		config.Tokens[tokenName] = tc
	}
	return nil
}

func applyTokenField(tc *TokenConfig, field, val string) error {
	switch field {
	case "INTERVAL_SECONDS":
		return parseIntInto(val, &tc.IntervalSeconds)
	case "ENABLED":
		return parseBoolInto(val, &tc.Enabled)
	case "TIMEOUT_SECONDS":
		return parseIntInto(val, &tc.TimeoutSeconds)
	case "MIN_TIME_RANGE_SECONDS":
		return parseIntInto(val, &tc.MinTimeRangeSeconds)
	case "LIVE_LOOKBACK_SECONDS":
		return parseIntInto(val, &tc.LiveLookbackSeconds)
	case "MAX_CHUNK_MINUTES":
		return parseIntInto(val, &tc.MaxChunkMinutes)
	case "BACKFILL_ENABLED":
		return parseBoolInto(val, &tc.Backfill.Enabled)
	case "BACKFILL_START_FROM":
		tc.Backfill.StartFrom = val
		return nil
	case "BACKFILL_CHUNK_MINUTES":
		return parseIntInto(val, &tc.Backfill.ChunkMinutes)
	default:
		return fmt.Errorf("unknown token field %q", field)
	}
}

func parseIntInto(s string, dst *int) error {
	v, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("invalid int %q: %w", s, err)
	}
	*dst = v
	return nil
}

func parseBoolInto(s string, dst *bool) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("invalid bool %q: %w", s, err)
	}
	*dst = v
	return nil
}
