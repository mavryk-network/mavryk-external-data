package config

// TickersConfig controls the CoinGecko tickers job and the in-process snapshot
// cache for /v1/tickers/:token/latest and /distribution.
//
// MVRK-only in v1: TokenSymbol is a single string. When we add a second token
// (USDT or DEX phase 2), flip to []string and reintroduce the concurrency cap
// used by JobConfig.
type TickersConfig struct {
	Enabled             bool               `yaml:"enabled"`
	TokenSymbol         string             `yaml:"token_symbol"`
	IntervalSeconds     int                `yaml:"interval_seconds"`
	HTTPTimeout         DurationYAML       `yaml:"http_timeout"`
	IncludeExchangeLogo bool               `yaml:"include_exchange_logo"`
	Cache               TickersCacheConfig `yaml:"cache"`
}

// TickersCacheConfig sets the per-endpoint cache TTL. Separate knobs because
// /distribution costs more to compute (GROUP BY) but is called less often, so
// a longer TTL is cheaper at the same hit ratio.
type TickersCacheConfig struct {
	LatestTTLSeconds       int `yaml:"latest_ttl_seconds"`
	DistributionTTLSeconds int `yaml:"distribution_ttl_seconds"`
}
