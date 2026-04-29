package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Load reads optional YAML from configPath, applies environment overrides and
// defaults, then runs Config.Validate. Returns a fully-resolved Config or an
// error suitable for fatal-startup logging (no silent partial state).
func Load(configPath string) (*Config, error) {
	if err := godotenv.Load(); err != nil {
		// .env is optional; not finding it is normal in containers.
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

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return config, nil
}
