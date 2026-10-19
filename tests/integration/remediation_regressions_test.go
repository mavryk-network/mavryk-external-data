//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/stretchr/testify/require"
)

// A 6h backfill-chunk window can never fully enclose a 1d bucket, and an
// unaligned refresh is rejected as "window too small".
func TestRefreshCandleAggregates_SubBucketWindowRefreshes1d(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)

	// Far-past day: materialization advances the view's watermark container-wide,
	// so a later one would drop sibling tests' rows from the real-time union.
	day := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		require.NoError(t, db.Exec(
			`INSERT INTO token_prices (token_symbol, source_code, ts, quote_currency, price)
			 VALUES ('mvrk', 'coingecko', ?, 'usd', ?)`,
			day.Add(time.Duration(i)*time.Hour), fmt.Sprintf("0.07%d", i)).Error)
	}

	repo := repositories.NewTokenPriceRepository(db)
	require.NoError(t, repo.RefreshCandleAggregates(context.Background(), day, day.Add(6*time.Hour)),
		"a sub-bucket window must be widened, not rejected")

	// Prove materialization (not just the real-time union).
	var matName string
	require.NoError(t, db.Raw(
		`SELECT materialization_hypertable_name
		   FROM timescaledb_information.continuous_aggregates
		  WHERE view_name = 'token_prices_1d'`).Scan(&matName).Error)
	require.NotEmpty(t, matName)
	var n int64
	require.NoError(t, db.Raw(
		fmt.Sprintf(`SELECT count(*) FROM _timescaledb_internal.%q`, matName)).Scan(&n).Error)
	require.Greater(t, n, int64(0), "the 1d bucket must be materialized by the widened refresh")
}

// Upstream had launches and none landed: without an error, job_last_success
// would stamp a healthy tick straight through a write outage. The catalog
// transaction must also roll back any pairs refreshed before that failure.
func TestSyncRWACatalog_FailedLaunchWriteReturnsError(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	cfg := launchSyncTestConfig(srv.URL)
	lookup := repositories.NewLookupRepository(db)
	ctx := context.Background()
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	list, err := repositories.NewLaunchRepository(db).EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, db.Exec(`CREATE FUNCTION fail_review_launch_write() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'simulated launch write failure'; END $$`).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec(`DROP TRIGGER IF EXISTS fail_review_launch_write ON rwa_launches`).Error)
		require.NoError(t, db.Exec(`DROP FUNCTION fail_review_launch_write()`).Error)
	})
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_review_launch_write BEFORE INSERT ON rwa_launches
 FOR EACH ROW EXECUTE FUNCTION fail_review_launch_write()`).Error)
	upstream.change(func() { upstream.bookID = 77 })
	err = jobs.SyncRWACatalog(ctx, cfg, lookup, nil)
	require.ErrorContains(t, err, "simulated launch write failure")
	pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	require.EqualValues(t, 7, *pair.EquiteezOrderbookID, "failed launch write must roll back the catalog snapshot")
}

func launchSyncTestConfig(indexerURL string) *config.Config {
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.Equiteez.IndexerURL = indexerURL
	cfg.Equiteez.AllowlistURL = indexerURL + "/allowlist"
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	cfg.API.TimeoutSeconds = 10
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	return cfg
}
