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
	Server    ServerConfig         `yaml:"server"`
	Database  DatabaseConfig       `yaml:"database"`
	Job       JobConfig            `yaml:"job"`
	API       APIConfig            `yaml:"api"`
	CoinGecko CoinGeckoConfig      `yaml:"coingecko"`
	Backfill  BackfillConfig       `yaml:"backfill"`
	Tokens    map[string]TokenConfig `yaml:"tokens"`
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
	IntervalSeconds int  `yaml:"interval_seconds"`
	Enabled         bool `yaml:"enabled"`
}

type APIConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
	RateLimitRPS   int `yaml:"rate_limit_rps"`
}

type CoinGeckoConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

type BackfillConfig struct {
	Enabled      bool   `yaml:"enabled"`
	StartFrom    string `yaml:"start_from"`    // ISO date or RFC3339 (e.g., 2025-09-18 or 2025-09-18T00:00:00Z)
	SleepMs      int    `yaml:"sleep_ms"`      // delay between backfill chunks in milliseconds
	ChunkMinutes int    `yaml:"chunk_minutes"` // size of each backfill window in minutes
}

type TokenConfig struct {
	IntervalSeconds      int                 `yaml:"interval_seconds"`      // Collection interval in seconds (0 = use global job.interval_seconds)
	Enabled              bool                `yaml:"enabled"`                // Enable/disable collection for this token (default: true)
	TimeoutSeconds       int                 `yaml:"timeout_seconds"`       // HTTP timeout in seconds (0 = use global api.timeout_seconds)
	MinTimeRangeSeconds  int                 `yaml:"min_time_range_seconds"` // Minimum time range to collect (0 = use default 60)
	MaxChunkMinutes      int                 `yaml:"max_chunk_minutes"`     // Maximum chunk size for catch-up (0 = use backfill.chunk_minutes or default 60)
	Backfill             TokenBackfillConfig `yaml:"backfill"`              // Token-specific backfill settings
}

type TokenBackfillConfig struct {
	Enabled      bool   `yaml:"enabled"`       // Enable/disable backfill for this token (default: false, uses global backfill.enabled if not set)
	StartFrom    string `yaml:"start_from"`   // Backfill start date for this token (ISO date or RFC3339, overrides global if set)
	SleepMs      int    `yaml:"sleep_ms"`     // Delay between backfill chunks in milliseconds (0 = use global backfill.sleep_ms)
	ChunkMinutes int    `yaml:"chunk_minutes"` // Size of each backfill window in minutes (0 = use global backfill.chunk_minutes)
}

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

	overrideWithEnv(config)

	setDefaults(config)

	return config, nil
}

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
}

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

	// Backfill defaults
	// Disabled by default; explicit opt-in
	// StartFrom default left empty (will be treated as no-op)
	if config.Backfill.SleepMs == 0 {
		config.Backfill.SleepMs = 3000
	}
	if config.Backfill.ChunkMinutes == 0 {
		config.Backfill.ChunkMinutes = 5
	}
}

func (c *Config) GetJobInterval() time.Duration {
	return time.Duration(c.Job.IntervalSeconds) * time.Second
}

func (c *Config) GetTokenConfig(tokenName string) TokenConfig {
	if c.Tokens == nil {
		c.Tokens = make(map[string]TokenConfig)
	}

	tokenCfg, exists := c.Tokens[tokenName]
	if !exists {
		return TokenConfig{
			IntervalSeconds:     0, // 0 means use global
			Enabled:             true,
			TimeoutSeconds:      0, // 0 means use global
			MinTimeRangeSeconds: 0, // 0 means use default 60
		}
	}

	if tokenCfg.IntervalSeconds == 0 {
		tokenCfg.IntervalSeconds = c.Job.IntervalSeconds
	}
	if tokenCfg.TimeoutSeconds == 0 {
		tokenCfg.TimeoutSeconds = c.API.TimeoutSeconds
	}
	if tokenCfg.MinTimeRangeSeconds == 0 {
		tokenCfg.MinTimeRangeSeconds = 60 // default 60 seconds
	}
	if tokenCfg.MaxChunkMinutes == 0 {
		// Use backfill chunk size or default to 60 minutes
		if c.Backfill.ChunkMinutes > 0 {
			tokenCfg.MaxChunkMinutes = c.Backfill.ChunkMinutes
		} else {
			tokenCfg.MaxChunkMinutes = 60 // default 60 minutes
		}
	}

	// Fill in backfill defaults
	if tokenCfg.Backfill.SleepMs == 0 {
		tokenCfg.Backfill.SleepMs = c.Backfill.SleepMs
		if tokenCfg.Backfill.SleepMs == 0 {
			tokenCfg.Backfill.SleepMs = 3000 // default 3 seconds
		}
	}
	if tokenCfg.Backfill.ChunkMinutes == 0 {
		tokenCfg.Backfill.ChunkMinutes = c.Backfill.ChunkMinutes
		if tokenCfg.Backfill.ChunkMinutes == 0 {
			tokenCfg.Backfill.ChunkMinutes = 5 // default 5 minutes
		}
	}
	if tokenCfg.Backfill.StartFrom == "" {
		tokenCfg.Backfill.StartFrom = c.Backfill.StartFrom
	}

	return tokenCfg
}

// IsTokenBackfillEnabled checks if backfill is enabled for a specific token
func (c *Config) IsTokenBackfillEnabled(tokenName string) bool {
	// First check if token collection is enabled
	if !c.IsTokenEnabled(tokenName) {
		return false // If token is disabled, backfill is also disabled
	}
	
	tokenCfg := c.GetTokenConfig(tokenName)
	
	// If backfill.enabled is explicitly false, disable backfill
	if !tokenCfg.Backfill.Enabled {
		return false
	}
	
	// If start_from is set, enable backfill (unless explicitly disabled above)
	if tokenCfg.Backfill.StartFrom != "" {
		return true
	}
	
	// If token-specific backfill.enabled is true, enable it
	if tokenCfg.Backfill.Enabled {
		return true
	}
	
	// Otherwise use global backfill setting
	return c.Backfill.Enabled
}

// GetTokenBackfillStartFrom returns the start date for token backfill
func (c *Config) GetTokenBackfillStartFrom(tokenName string) string {
	tokenCfg := c.GetTokenConfig(tokenName)
	if tokenCfg.Backfill.StartFrom != "" {
		return tokenCfg.Backfill.StartFrom
	}
	return c.Backfill.StartFrom
}

func (c *Config) GetTokenInterval(tokenName string) time.Duration {
	tokenCfg := c.GetTokenConfig(tokenName)
	return time.Duration(tokenCfg.IntervalSeconds) * time.Second
}

func (c *Config) GetTokenTimeout(tokenName string) time.Duration {
	tokenCfg := c.GetTokenConfig(tokenName)
	return time.Duration(tokenCfg.TimeoutSeconds) * time.Second
}

func (c *Config) IsTokenEnabled(tokenName string) bool {
	tokenCfg := c.GetTokenConfig(tokenName)
	return tokenCfg.Enabled
}
