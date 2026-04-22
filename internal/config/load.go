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

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return config, nil
}
