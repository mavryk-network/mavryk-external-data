//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openGorm returns a *gorm.DB connected to the test container. The DSN comes
// from setup_test.go (TestMain populates pgDSN). Each test gets its own gorm
// session — connections are pooled by pgx underneath.
func openGorm(t *testing.T) *gorm.DB {
	t.Helper()
	require.NotEmpty(t, pgDSN, "pgDSN unset — TestMain didn't run; missing build tag?")

	db, err := gorm.Open(postgres.Open(pgDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// truncateTokenPrices resets the FA hot path so tests can stand up
// independent fixtures. CASCADE flushes any continuous-aggregate
// materialised rows that referenced the tick rows.
//
// For a TimescaleDB hypertable, TRUNCATE is the cheapest way to clear
// chunks; the seed lookup tables (tokens, sources) stay intact so token
// FK constraints continue to resolve.
func truncateTokenPrices(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`TRUNCATE TABLE token_prices`).Error)
	// Continuous aggregates need explicit refresh after a wipe; tests
	// that depend on the CA call refreshTokenCA explicitly after seeding.
}

// refreshCA forces the named continuous aggregate to materialise the given
// window. With end_offset on the policy you'd never see the latest minute;
// the manual call honours the explicit bounds passed here. NULL bounds
// mean "the whole materialised range" — fine for tests.
//
// Works for both FA (`token_prices_*`) and RWA (`rwa_quote_prices_*`) CAs.
func refreshCA(t *testing.T, db *gorm.DB, view string) {
	t.Helper()
	require.NoError(t,
		db.Exec(`CALL refresh_continuous_aggregate(?, NULL, NULL)`, view).Error)
}
