package config

// Config is the root application configuration (YAML + environment).
type Config struct {
	Server    ServerConfig           `yaml:"server"`
	Database  DatabaseConfig         `yaml:"database"`
	Job       JobConfig              `yaml:"job"`
	API       APIConfig              `yaml:"api"`
	CoinGecko CoinGeckoConfig        `yaml:"coingecko"`
	Backfill  BackfillConfig         `yaml:"backfill"`
	Tokens    map[string]TokenConfig `yaml:"tokens"`
}
