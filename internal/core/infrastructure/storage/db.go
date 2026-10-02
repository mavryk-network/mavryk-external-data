package storage

import (
	"fmt"
	"quotes/internal/config"

	"github.com/rs/zerolog"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type DB struct {
	*gorm.DB
}

// NewDB opens the database, bounds SQL execution on every pooled connection,
// configures pool sizing per cfg.Database, and verifies connectivity (Ping).
func NewDB(cfg *config.Config, log *zerolog.Logger) (*DB, error) {
	statementTimeoutMS, err := cfg.Database.StatementTimeoutMilliseconds()
	if err != nil {
		return nil, err
	}
	sslMode := cfg.Database.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	// A startup parameter applies to every connection the pool opens, unlike
	// a one-off SET executed against whichever connection happens to be idle.
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=UTC statement_timeout=%d",
		cfg.Database.Host,
		cfg.Database.User,
		cfg.Database.Password,
		cfg.Database.Name,
		cfg.Database.Port,
		sslMode,
		statementTimeoutMS,
	)

	logMode := logger.Silent
	if cfg.Database.Logging {
		logMode = logger.Info
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logMode),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB handle: %w", err)
	}
	if cfg.Database.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	}
	if d := cfg.Database.ConnMaxLifetime.D(); d > 0 {
		sqlDB.SetConnMaxLifetime(d)
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("database ping failed: %w", err)
	}

	if log != nil {
		log.Info().
			Int("max_open_conns", cfg.Database.MaxOpenConns).
			Int("max_idle_conns", cfg.Database.MaxIdleConns).
			Dur("conn_max_lifetime", cfg.Database.ConnMaxLifetime.D()).
			Int64("statement_timeout_ms", statementTimeoutMS).
			Msg("database_connected")
	}
	return &DB{DB: db}, nil
}

func (db *DB) Close() error {
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// BatchSize returns the configured batch size or the default fallback.
func BatchSize(cfg *config.Config) int {
	const fallback = 500
	if cfg == nil || cfg.Database.BatchSize <= 0 {
		return fallback
	}
	return cfg.Database.BatchSize
}
