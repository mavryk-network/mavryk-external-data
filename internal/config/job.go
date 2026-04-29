package config

// JobConfig controls the FT live-collector job defaults.
//
// Concurrency bounds the number of tokens collected in parallel each tick.
// 0 = unlimited (one goroutine per token, legacy behaviour); production should
// pin to e.g. 2–4 so a slow CoinGecko call doesn't fan-out across all tokens
// simultaneously.
type JobConfig struct {
	IntervalSeconds int  `yaml:"interval_seconds"`
	Enabled         bool `yaml:"enabled"`
	Concurrency     int  `yaml:"concurrency"`
}
