//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

const (
	prepareLatestIndexMigration = "0022_prepare_token_prices_latest_index.sql"
	buildLatestIndexMigration   = "0023_token_prices_latest_index.sql"
	retireLatestIndexMigration  = "0024_drop_superseded_token_prices_index.sql"
)

func migrationSQL(t *testing.T, name string) string {
	t.Helper()
	dir, err := findMigrationsDir()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(body)
}

func latestIndexState(t *testing.T, db *sql.DB, name string) (oid int64, valid bool) {
	t.Helper()
	rows, err := db.Query(`SELECT indexrelid::bigint, indisvalid AND indisready
		FROM pg_index WHERE indexrelid = to_regclass($1)`, name)
	require.NoError(t, err)
	defer rows.Close()
	if rows.Next() {
		require.NoError(t, rows.Scan(&oid, &valid))
	}
	require.NoError(t, rows.Err())
	return oid, valid
}

// Reproduce an already-deployed schema with data spread across real chunks.
func legacyLatestIndexDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db := openGorm(t)
	truncateTokenPrices(t, db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	_, err = sqlDB.Exec(`DROP INDEX IF EXISTS idx_token_prices_latest_source`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_token_prices_latest
		ON token_prices (token_symbol, quote_currency, ts DESC)`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`INSERT INTO token_prices
		(token_symbol, source_code, ts, quote_currency, price)
		SELECT 'mvrk', 'coingecko', now() - n * interval '8 days', 'usd', 1
		FROM generate_series(0, 2) AS n`)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, name := range []string{prepareLatestIndexMigration, buildLatestIndexMigration, retireLatestIndexMigration} {
			_, restoreErr := sqlDB.Exec(migrationSQL(t, name))
			require.NoErrorf(t, restoreErr, "restore %s", name)
		}
	})
	return sqlDB
}

func TestTokenPricesLatestIndexSwappedOnReplay(t *testing.T) {
	db := legacyLatestIndexDatabase(t)
	dir, err := findMigrationsDir()
	require.NoError(t, err)
	legacyOID, valid := latestIndexState(t, db, "idx_token_prices_latest")
	require.True(t, valid)

	// Even an out-of-order cleanup must preserve the only working index.
	_, err = db.Exec(migrationSQL(t, retireLatestIndexMigration))
	require.ErrorContains(t, err, "missing or invalid")

	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	require.NoError(t, err)
	sort.Strings(files)
	var replacementOID int64
	for replay := 0; replay < 2; replay++ {
		for _, path := range files {
			name := filepath.Base(path)
			if name == retireLatestIndexMigration {
				_, ready := latestIndexState(t, db, "idx_token_prices_latest_source")
				require.True(t, ready, "replacement must be usable before legacy cleanup")
			}
			_, err = db.Exec(migrationSQL(t, name))
			require.NoErrorf(t, err, "applying %s (replay %d)", name, replay)
			if replay == 0 && name < retireLatestIndexMigration {
				oid, usable := latestIndexState(t, db, "idx_token_prices_latest")
				require.Equal(t, legacyOID, oid, "legacy index must survive until replacement is complete")
				require.True(t, usable)
			}
		}
		oid, usable := latestIndexState(t, db, "idx_token_prices_latest_source")
		require.True(t, usable)
		if replay == 0 {
			replacementOID = oid
		} else {
			require.Equal(t, replacementOID, oid, "a valid replacement must not rebuild on replay")
		}
		oldOID, _ := latestIndexState(t, db, "idx_token_prices_latest")
		require.Zero(t, oldOID)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM token_prices`).Scan(&count))
	require.Equal(t, 3, count, "the upgrade must preserve existing data")
}

func TestTokenPricesLatestIndexRecoversInterruptedBuild(t *testing.T) {
	db := legacyLatestIndexDatabase(t)
	ctx := context.Background()
	var schema, chunk string
	require.NoError(t, db.QueryRow(`SELECT chunk_schema, chunk_name
		FROM timescaledb_information.chunks WHERE hypertable_name = 'token_prices'
		ORDER BY range_start LIMIT 1`).Scan(&schema, &chunk))

	// Force a real per-chunk failure after Timescale commits the invalid parent,
	// rather than synthesizing the state by editing PostgreSQL's catalogs.
	blocker, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer blocker.Rollback()
	_, err = blocker.Exec(`LOCK TABLE ` + pgx.Identifier{schema, chunk}.Sanitize() + ` IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	builder, err := db.Conn(ctx)
	require.NoError(t, err)
	defer builder.Close()
	_, err = builder.ExecContext(ctx, `SET lock_timeout = '250ms'`)
	require.NoError(t, err)
	_, err = builder.ExecContext(ctx, migrationSQL(t, buildLatestIndexMigration))
	require.ErrorContains(t, err, "lock timeout")
	require.NoError(t, blocker.Rollback())
	_, err = builder.ExecContext(ctx, `RESET lock_timeout`)
	require.NoError(t, err)

	invalidOID, valid := latestIndexState(t, db, "idx_token_prices_latest_source")
	require.NotZero(t, invalidOID, "interrupted build must leave an index for recovery to repair")
	require.False(t, valid)
	_, err = db.Exec(migrationSQL(t, retireLatestIndexMigration))
	require.ErrorContains(t, err, "missing or invalid")
	_, valid = latestIndexState(t, db, "idx_token_prices_latest")
	require.True(t, valid, "failed replacement must leave the legacy index usable")

	for _, name := range []string{prepareLatestIndexMigration, buildLatestIndexMigration, retireLatestIndexMigration} {
		_, err = db.Exec(migrationSQL(t, name))
		require.NoErrorf(t, err, "retry %s", name)
	}
	replacementOID, valid := latestIndexState(t, db, "idx_token_prices_latest_source")
	require.True(t, valid)
	require.NotEqual(t, invalidOID, replacementOID, "recovery must rebuild the invalid index")
	oldOID, _ := latestIndexState(t, db, "idx_token_prices_latest")
	require.Zero(t, oldOID)

	// Verify writes still work after both the failed attempt and recovery.
	_, err = db.Exec(`INSERT INTO token_prices
		(token_symbol, source_code, ts, quote_currency, price)
		VALUES ('mvrk', 'coingecko', $1, 'usd', 2)`, time.Now().UTC().Add(time.Second))
	require.NoError(t, err)
}
