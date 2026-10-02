package config

import (
	"fmt"
	"time"
)

const DefaultStatementTimeout = 10 * time.Second

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
	// StatementTimeout bounds each application SQL statement, including jobs.
	// Zero uses DefaultStatementTimeout; negative values are rejected.
	StatementTimeout DurationYAML `yaml:"statement_timeout"`
	// BatchSize bounds rows per CreateInBatches call across all repositories.
	// 0 means use the in-code default (500).
	BatchSize int `yaml:"batch_size"`
}

// StatementTimeoutMilliseconds returns PostgreSQL's startup parameter value.
// Validate before truncating to milliseconds: a sub-millisecond value would
// become zero, which disables the database timeout.
func (c DatabaseConfig) StatementTimeoutMilliseconds() (int64, error) {
	d := c.StatementTimeout.D()
	if d == 0 {
		d = DefaultStatementTimeout
	}
	const maxTimeout = time.Duration(1<<31-1) * time.Millisecond
	if d < time.Millisecond || d > maxTimeout {
		return 0, fmt.Errorf("database.statement_timeout must be between 1ms and %s (0 uses %s), got %s", maxTimeout, DefaultStatementTimeout, d)
	}
	return d.Milliseconds(), nil
}
