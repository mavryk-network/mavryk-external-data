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

// NewDB opens the database, configures pool sizing per cfg.Database, and verifies
// connectivity (Ping). Returns a wrapped *gorm.DB.
func NewDB(cfg *config.Config, log *zerolog.Logger) (*DB, error) {
	sslMode := cfg.Database.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s TimeZone=UTC",
		cfg.Database.Host,
		cfg.Database.User,
		cfg.Database.Password,
		cfg.Database.Name,
		cfg.Database.Port,
		sslMode,
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
