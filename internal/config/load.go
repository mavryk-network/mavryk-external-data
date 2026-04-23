package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Load reads optional YAML from configPath, applies environment overrides and defaults, then Config.Validate.
func Load(configPath string) (*Config, error) {
	if err := godotenv.Load(); err != nil {
		fmt.Println("No .env file found, using environment variables only")
	}

	config := &Config{}
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		if err := yaml.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	if err := overrideWithEnv(config); err != nil {
		return nil, fmt.Errorf("environment overrides: %w", err)
	}

	setDefaults(config)

	// After defaults: explicit 0 in env disables the response cache.
	if v := os.Getenv("SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS"); v != "" {
		if val, err := strconv.Atoi(v); err == nil {
			config.Server.LatestQuoteCacheTTLSeconds = val
		}
	}
	// After defaults: explicit 0 in env disables the per-service rate limiter.
	// setDefaults picks a safe 8 rps for CoinGecko when unset, so a plain pre-defaults
	// override can't get you to 0. Equiteez defaults to 0 already; this hatch just
	// keeps the pattern symmetrical for future tuning from ops.
	applyRateLimitEnvOverride("COINGECKO_RATE_LIMIT_RPS", &config.CoinGecko.RateLimit.RPS)
	applyRateLimitEnvOverride("EQUITEEZ_RATE_LIMIT_RPS", &config.Equiteez.RateLimit.RPS)

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return config, nil
}

// applyRateLimitEnvOverride lets ops set a per-service RPS to 0 after defaults
// are applied. Silently ignores unset or malformed values (validator catches
// negatives downstream).
func applyRateLimitEnvOverride(envKey string, dst *float64) {
	v := os.Getenv(envKey)
	if v == "" {
		return
	}
	val, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return
	}
	*dst = val
}
