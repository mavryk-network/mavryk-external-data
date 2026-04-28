package config

// DatabaseConfig holds PostgreSQL connection settings + pool tuning.
//
// Pool tuning knobs (refactoring_v2 §4.4): GORM's defaults pin MaxOpenConns to
// roughly the runtime's GOMAXPROCS, which is too narrow for a service that runs
// HTTP + 3 jobs concurrently. Set explicit values for production.
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Name     string `yaml:"name"`
	SSLMode  string `yaml:"ssl_mode"`
	Logging  bool   `yaml:"logging"`
	// MaxOpenConns / MaxIdleConns / ConnMaxLifetime — sql.DB pool tuning. 0
	// keeps the driver default. Reasonable production values: 25/5/30m.
	MaxOpenConns    int          `yaml:"max_open_conns"`
	MaxIdleConns    int          `yaml:"max_idle_conns"`
	ConnMaxLifetime DurationYAML `yaml:"conn_max_lifetime"`
	// BatchSize bounds rows per CreateInBatches call across all repositories.
	// 0 means use the in-code default (500).
	BatchSize int `yaml:"batch_size"`
}
