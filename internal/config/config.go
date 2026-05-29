package config

// Config is the root application configuration (YAML + environment).
type Config struct {
	Server    ServerConfig           `yaml:"server"`
	Database  DatabaseConfig         `yaml:"database"`
	Job       JobConfig              `yaml:"job"`
	API       APIConfig              `yaml:"api"`
	CoinGecko CoinGeckoConfig        `yaml:"coingecko"`
	Equiteez  EquiteezConfig         `yaml:"equiteez"`
	Backfill  BackfillConfig         `yaml:"backfill"`
	RWA       RWAConfig              `yaml:"rwa"`
	Tickers   TickersConfig          `yaml:"tickers"`
	Auth      AuthConfig             `yaml:"auth"`
	Tokens    map[string]TokenConfig `yaml:"tokens"`
}
