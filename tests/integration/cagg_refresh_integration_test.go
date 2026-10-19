//go:build integration

package integration

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
)

func TestCandleRefreshRecoversAfterProbeFailure(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	lookup := repositories.NewLookupRepository(db)
	history := repositories.NewRWAPriceRepository(db)
	pairID := insertRWAPair(t, db, "recovery", "usdt")
	stamp := time.Date(2025, 1, 2, 12, 34, 56, 0, time.UTC)
	fixtureSaveRWAPoints(t, history, pairID, prices.SideLast, stamp, []decimal.Decimal{dec("42")})

	// A failed extension probe must be returned to the worker so it cannot
	// checkpoint history whose chart buckets have not been materialized.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err := lookup.RefreshRWACandleAggregates(cancelled, stamp, stamp.Add(time.Second))
	require.Error(t, err)

	// A later healthy attempt must retry the probe and refresh all bucket sizes.
	require.NoError(t, lookup.RefreshRWACandleAggregates(context.Background(), stamp, stamp.Add(time.Second)))
	for _, interval := range []string{"1m", "1h", "1d"} {
		t.Run(interval, func(t *testing.T) {
			candles, err := history.QueryCandles(context.Background(), apiprices.CandleQuery{
				EntityKey: strconv.FormatInt(pairID, 10), AuxKey: "last",
				Interval: apiprices.Interval(interval), From: stamp.Truncate(24 * time.Hour), To: stamp.Truncate(24 * time.Hour).Add(24 * time.Hour), Limit: 100,
			})
			require.NoError(t, err)
			require.Len(t, candles, 1)
			require.True(t, candles[0].Close.Equal(dec("42")))
		})
	}
}

func TestCandleRefreshSlicesMultiDayWindow(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	history := repositories.NewRWAPriceRepository(db)
	pairID := insertRWAPair(t, db, "bounded-refresh", "usdt")
	start := time.Date(2021, 1, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(50 * time.Hour)
	fixtureSaveRWAPoints(t, history, pairID, prices.SideLast, start, []decimal.Decimal{dec("42")})
	fixtureSaveRWAPoints(t, history, pairID, prices.SideLast, end.Add(-time.Hour), []decimal.Decimal{dec("43")})
	calls := 0
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:bounded_cagg_windows", func(tx *gorm.DB) {
		if !strings.HasPrefix(tx.Statement.SQL.String(), "CALL refresh_continuous_aggregate") {
			return
		}
		calls++
		require.Len(t, tx.Statement.Vars, 2)
		from := tx.Statement.Vars[0].(time.Time)
		to := tx.Statement.Vars[1].(time.Time)
		require.True(t, to.After(from))
		require.LessOrEqual(t, to.Sub(from), 24*time.Hour, "each real SQL statement must remain bounded")
	}))
	require.NoError(t, repositories.NewLookupRepository(db).RefreshRWACandleAggregates(context.Background(), start, end))
	require.Greater(t, calls, 3, "a multi-day refresh must be sliced for each aggregate")
	for _, interval := range []apiprices.Interval{apiprices.Interval1m, apiprices.Interval1h, apiprices.Interval1d} {
		candles, err := history.QueryCandles(context.Background(), apiprices.CandleQuery{EntityKey: strconv.FormatInt(pairID, 10), AuxKey: "last", Interval: interval, From: start.Truncate(24 * time.Hour), To: end.Truncate(24 * time.Hour).Add(24 * time.Hour), Limit: 10})
		require.NoError(t, err)
		require.Len(t, candles, 2)
		require.Equal(t, "42", candles[0].Close.String())
		require.Equal(t, "43", candles[1].Close.String())
	}
}
