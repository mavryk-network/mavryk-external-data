//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage"
	"quotes/internal/core/infrastructure/storage/repositories"
)

func TestEquiteezBackfill_RefreshTimeoutRetriesBeforeCheckpointingSparseFills(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	stamps := []string{"2025-03-01T12:00:00Z", "2025-03-01T13:00:00Z", "2025-06-01T14:00:00Z"}
	requests := make(chan recoveryBoundaryRequest, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allowlist" {
			upstream.ServeHTTP(w, r)
			return
		}
		req, err := readRecoveryBoundaryRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Query, "filledOrderbookOrders") {
			upstream.ServeHTTP(w, r)
			return
		}
		since, _ := req.Variables["since_id"].(float64)
		rows := []any{}
		for i, stamp := range stamps {
			id := 41 + i
			if float64(id) <= since {
				continue
			}
			rows = append(rows, map[string]any{"id": id, "order_type": 1, "price_per_rwa_token": "2000000", "fulfilled_amount": "1000000", "ended_at": stamp})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"orderbook_order": rows}})
		requests <- req
	}))
	t.Cleanup(srv.Close)
	cfg := recoveryBoundaryConfig(srv.URL)
	ctx := context.Background()
	lookup := repositories.NewLookupRepository(db)
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	tokens, err := lookup.Tokens(ctx)
	require.NoError(t, err)
	prices.RegisterTokens(tokens)

	// Use the real app DSN timeout on every worker connection, and block only
	// the candle materialization table. Raw inserts can still succeed.
	pg, err := pgx.ParseConfig(pgDSN)
	require.NoError(t, err)
	cfg.Database = config.DatabaseConfig{Host: pg.Host, Port: strconv.Itoa(int(pg.Port)), User: pg.User, Password: pg.Password, Name: pg.Database, SSLMode: "disable", MaxOpenConns: 5, StatementTimeout: config.DurationYAML(500 * time.Millisecond)}
	log := zerolog.Nop()
	bounded, err := storage.NewDB(cfg, &log)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, bounded.Close()) })
	lookup = repositories.NewLookupRepository(bounded.DB)
	history := repositories.NewRWAPriceRepository(bounded.DB)
	state := repositories.NewBackfillStateRepository(bounded.DB)
	cfg.Equiteez.Backfill.Enabled = true
	cfg.Equiteez.Backfill.TickSeconds = 3600
	runStep := func() recoveryBoundaryRequest {
		t.Helper()
		job := jobs.NewEquiteezBackfillJob(cfg, history, lookup, state, nil)
		t.Cleanup(job.Stop)
		job.Start(ctx)
		select {
		case req := <-requests:
			job.Stop()
			return req
		case <-time.After(15 * time.Second):
			t.Fatal("backfill never fetched a batch")
			return recoveryBoundaryRequest{}
		}
	}
	var matName string
	require.NoError(t, db.Raw("SELECT materialization_hypertable_name FROM timescaledb_information.continuous_aggregates WHERE view_name='rwa_quote_prices_1m'").Scan(&matName).Error)
	require.NotEmpty(t, matName)
	lock := db.Begin()
	require.NoError(t, lock.Error)
	t.Cleanup(func() { _ = lock.Rollback().Error })
	require.NoError(t, lock.Exec(fmt.Sprintf("LOCK TABLE _timescaledb_internal.%q IN ACCESS EXCLUSIVE MODE", matName)).Error)
	req := runStep()
	require.NotContains(t, req.Variables, "since_id")
	require.NoError(t, lock.Rollback().Error)
	failed, err := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
	require.NoError(t, err)
	require.NotNil(t, failed)
	require.Nil(t, failed.CursorID, "a timed-out refresh must not checkpoint raw-only history")
	require.Nil(t, failed.CursorTs)
	require.Contains(t, failed.LastError, "refresh rwa_quote_prices_1m [2025-03-01T12:00:00Z,")
	require.Contains(t, failed.LastError, "statement timeout")
	require.NotNil(t, failed.NextAttemptAt)
	var count int64
	require.NoError(t, db.Table("rwa_quote_prices").Where("pair_id=?", pair.ID).Count(&count).Error)
	require.EqualValues(t, 2, count, "only the first fill day is saved before refresh")

	// Skip waiting for the retry delay, then prove the same day is retried and
	// materialized before moving to June. No multi-month refresh is needed.
	require.NoError(t, db.Exec("UPDATE backfill_state SET next_attempt_at=NULL WHERE source_code='equiteez' AND entity_key=?", pair.EntityKey()).Error)
	req = runStep()
	require.NotContains(t, req.Variables, "since_id", "retry must re-read the uncheckpointed batch")
	saved, err := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
	require.NoError(t, err)
	require.NotNil(t, saved.CursorID)
	require.EqualValues(t, 42, *saved.CursorID)
	require.Empty(t, saved.LastError)
	require.Nil(t, saved.NextAttemptAt)
	req = runStep()
	require.Equal(t, float64(42), req.Variables["since_id"])
	saved, err = state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
	require.NoError(t, err)
	require.EqualValues(t, 43, *saved.CursorID)
	for _, day := range []time.Time{time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)} {
		candles, err := history.QueryCandles(ctx, apiprices.CandleQuery{EntityKey: pair.EntityKey(), AuxKey: "last", Interval: apiprices.Interval1d, From: day, To: day.Add(24 * time.Hour), Limit: 10})
		require.NoError(t, err)
		require.Len(t, candles, 1)
		require.Equal(t, "2", candles[0].Close.String())
	}
}
