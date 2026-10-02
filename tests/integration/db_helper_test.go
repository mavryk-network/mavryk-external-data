//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openGorm connects to the shared test container; TestMain (setup_test.go)
// populates pgDSN.
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

// TRUNCATE is the cheapest way to clear a hypertable's chunks; the seed lookup
// tables (tokens, sources) stay intact so FK constraints keep resolving.
// Continuous aggregates are NOT refreshed — callers do that via refreshCA.
func truncateTokenPrices(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`TRUNCATE TABLE token_prices`).Error)
}

// refreshCA materialises a continuous aggregate over its whole range (NULL
// bounds), bypassing the policy's end_offset which would hide the latest
// minute. Works for both token_prices_* and rwa_quote_prices_* views.
func refreshCA(t *testing.T, db *gorm.DB, view string) {
	t.Helper()
	require.NoError(t,
		db.Exec(`CALL refresh_continuous_aggregate(?, NULL, NULL)`, view).Error)
}

// recordingGorm captures the statements gorm emits (bindings inlined) so a test
// can EXPLAIN the exact SQL a repository produced, not a hand-copy of it.
func recordingGorm(t *testing.T, rec *legacySQLRecorder) *gorm.DB {
	t.Helper()
	require.NotEmpty(t, pgDSN, "pgDSN unset — TestMain didn't run; missing build tag?")
	db, err := gorm.Open(postgres.Open(pgDSN), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
