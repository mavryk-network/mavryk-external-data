//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

// Answers both of SyncRWALaunches' queries: allowlist sweep, then launch fetch.
func fakeEquiteezIndexer(t *testing.T) *httptest.Server {
	t.Helper()
	const tokensResp = `{"data":{"token":[{
		"address":"KT1RegressionTestToken","token_id":0,"in_allowlist":true,
		"token_metadata":{"symbol":"RGT"},"orderbooks":[]}]}}`
	const launchesResp = `{"data":{"launchpad_launch":[{
		"id":91,"name":"RGT-issuance","status":1,
		"max_amount_cap":"1000000","total_bought":"10","is_paused":false,
		"updated_at":"2026-08-01T00:00:00Z",
		"token":{"address":"KT1RegressionTestToken","token_id":0},
		"sale_options":[{"name":"Base","total_bought":"10","max_amount_cap":"1000000","is_paused":false,
			"payments":[{"name":"USDT","price":"100","token":{"address":"KT1RegressionQuote","token_id":0}}]}]}]}}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read query body: %v", err)
		}
		body := string(raw)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, "allowlistedTokensWithOrderbooks"):
			_, _ = w.Write([]byte(tokensResp))
		case strings.Contains(body, "GetLaunchesByTokens"):
			_, _ = w.Write([]byte(launchesResp))
		default:
			t.Errorf("unexpected GraphQL query: %s", body)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

func launchSyncTestConfig(indexerURL string) *config.Config {
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.Equiteez.IndexerURL = indexerURL
	cfg.API.TimeoutSeconds = 10
	return cfg
}

// Upstream had launches and none landed: without an error, job_last_success
// would stamp a healthy tick straight through a write outage.
func TestSyncRWALaunches_AllUpsertsFailedReturnsError(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	srv := fakeEquiteezIndexer(t)
	defer srv.Close()
	cfg := launchSyncTestConfig(srv.URL)
	nop := zerolog.Nop()

	t.Run("happy path stores the launch", func(t *testing.T) {
		stored, err := jobs.SyncRWALaunches(context.Background(), cfg, repositories.NewLaunchRepository(db), &nop)
		require.NoError(t, err)
		require.Equal(t, 1, stored, "the canned launch must land")
	})

	t.Run("all upserts failing is an error", func(t *testing.T) {
		txErr := db.Transaction(func(tx *gorm.DB) error {
			// Sabotage writes inside the tx only; rollback restores the table.
			require.NoError(t, tx.Exec(`ALTER TABLE rwa_launches DROP COLUMN base_symbol`).Error)
			_, err := jobs.SyncRWALaunches(context.Background(), cfg, repositories.NewLaunchRepository(tx), &nop)
			require.Error(t, err, "a sync that refreshed nothing must not report success")
			require.Contains(t, err.Error(), "upserts failed")
			return fmt.Errorf("rollback the sabotage")
		})
		require.Error(t, txErr) // the deliberate rollback
	})
}
