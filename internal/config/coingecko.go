package config

// CoinGeckoConfig holds CoinGecko API client settings.
//
// RateLimit is a per-service outbound throttle (req/s + burst). When unset it
// defaults to a safe value for the Pro Analyst tier (500 req/min ≈ 8 rps).
// Shared across every CoinGecko client/backfill worker in-process via the
// component key "coingecko".
type CoinGeckoConfig struct {
	APIKey    string          `yaml:"api_key"`
	BaseURL   string          `yaml:"base_url"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}
