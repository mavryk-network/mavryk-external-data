package config

// CoinGeckoConfig holds CoinGecko API client settings.
type CoinGeckoConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}
