//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

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
