package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Job      JobConfig      `yaml:"job"`
	API      APIConfig      `yaml:"api"`
	CoinGecko CoinGeckoConfig `yaml:"coingecko"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
	Host string `yaml:"host"`
}

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Name     string `yaml:"name"`
	SSLMode  string `yaml:"ssl_mode"`
	Logging  bool   `yaml:"logging"`
}

type JobConfig struct {
	IntervalSeconds int `yaml:"interval_seconds"`
	Enabled         bool `yaml:"enabled"`
}

type APIConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
	RateLimitRPS   int `yaml:"rate_limit_rps"`
}

type CoinGeckoConfig struct {
	APIKey string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

func Load(configPath string) (*Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		// .env file is optional
		fmt.Println("No .env file found, using environment variables only")
	}

	// Load YAML config
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

	// Override with environment variables
	overrideWithEnv(config)

	// Set defaults
	setDefaults(config)

	return config, nil
}

// overrideWithEnv overrides config values with environment variables
func overrideWithEnv(config *Config) {
	if port := os.Getenv("SERVER_PORT"); port != "" {
		config.Server.Port = port
	}
	if host := os.Getenv("SERVER_HOST"); host != "" {
		config.Server.Host = host
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

	if apiKey := os.Getenv("COINGECKO_API_KEY"); apiKey != "" {
		config.CoinGecko.APIKey = apiKey
	}
	if baseURL := os.Getenv("COINGECKO_BASE_URL"); baseURL != "" {
		config.CoinGecko.BaseURL = baseURL
	}
}

// setDefaults sets default values for configuration
func setDefaults(config *Config) {
	if config.Server.Port == "" {
		config.Server.Port = "3010"
	}
	if config.Server.Host == "" {
		config.Server.Host = "0.0.0.0"
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
		config.Job.IntervalSeconds = 60 // 1 minute
	}

	if config.API.TimeoutSeconds == 0 {
		config.API.TimeoutSeconds = 30
	}
	if config.API.RateLimitRPS == 0 {
		config.API.RateLimitRPS = 100
	}

	if config.CoinGecko.BaseURL == "" {
		config.CoinGecko.BaseURL = "https://api.coingecko.com/api/v3"
	}
}

func (c *Config) GetJobInterval() time.Duration {
	return time.Duration(c.Job.IntervalSeconds) * time.Second
}
