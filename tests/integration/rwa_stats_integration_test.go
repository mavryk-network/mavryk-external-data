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

// These tests spell out multi-day timestamps because the shared fixture helpers
// space samples one second apart, landing everything in a single day bucket.

type statTick struct {
	ts    time.Time
	price decimal.Decimal
}

// Writes through the production Save path — the collector's own column mapping.
func saveRWAStatTicks(
	t *testing.T,
	repo *repositories.RWAPriceRepository,
	pairID int64,
	side prices.Side,
	ticks []statTick,
) {
	t.Helper()
	pts := make([]prices.PricePoint, 0, len(ticks))
	pidStr := strconv.FormatInt(pairID, 10)
	for _, tk := range ticks {
		pts = append(pts, prices.PricePoint{
			Source:    prices.SourceEquiteez,
			EntityKey: pidStr,
			Timestamp: tk.ts,
			Metric:    string(side),
			Price:     tk.price,
		})
	}
	n, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(len(pts)), n)
}

// The FA counterpart, on the seeded (mvrk, coingecko, usd).
func saveTokenStatTicks(t *testing.T, repo *repositories.TokenPriceRepository, ticks []statTick) {
	t.Helper()
	pts := make([]prices.PricePoint, 0, len(ticks))
	for _, tk := range ticks {
		pts = append(pts, prices.PricePoint{
			Source:    prices.SourceCoinGecko,
			EntityKey: "mvrk",
			Timestamp: tk.ts,
			Metric:    "usd",
			Price:     tk.price,
		})
	}
	n, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(len(pts)), n)
}

func utcDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestRWAAllTimeHighLast_ReturnsDayBucketNotSampleTS(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	day1 := utcDay(2026, 6, 1)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	athSample := day2.Add(13*time.Hour + 47*time.Minute)

	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{day1.Add(9 * time.Hour), dec("100.00")},
		{day2.Add(2 * time.Hour), dec("120.00")},
		{athSample, dec("180.25")},
		{day2.Add(18 * time.Hour), dec("140.00")},
		{day3.Add(11 * time.Hour), dec("150.00")},
	})
	refreshCA(t, db, "rwa_quote_prices_1d")

	price, ts, found, err := repo.AllTimeHighLast(context.Background(), pid, string(prices.SideLast))
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.Equal(dec("180.25")), "price=%s", price)
	require.True(t, ts.Equal(day2), "want the day bucket %s, got %s", day2, ts)
	require.False(t, ts.Equal(athSample), "ATH date must be the bucket, not the sample ts")
	require.Equal(t, time.UTC, ts.Location())
}

func TestRWAAllTimeHighLast_NoRows_NotFound(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	refreshCA(t, db, "rwa_quote_prices_1d")

	price, ts, found, err := repo.AllTimeHighLast(context.Background(), pid, string(prices.SideLast))
	require.NoError(t, err)
	require.False(t, found)
	require.True(t, price.IsZero())
	require.True(t, ts.IsZero())
}

func TestRWAAllTimeHighLast_IsolatedBySideAndPair(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	p1 := insertRWAPair(t, db, "mars1", "usdt")
	p2 := insertRWAPair(t, db, "mars2", "usdt")

	day1 := utcDay(2026, 6, 5)
	day2 := day1.AddDate(0, 0, 1)

	saveRWAStatTicks(t, repo, p1, prices.SideLast, []statTick{
		{day1.Add(10 * time.Hour), dec("100.00")},
		{day2.Add(10 * time.Hour), dec("120.00")},
	})
	saveRWAStatTicks(t, repo, p1, prices.SideBid, []statTick{
		{day1.Add(11 * time.Hour), dec("999.00")},
	})
	saveRWAStatTicks(t, repo, p2, prices.SideLast, []statTick{
		{day1.Add(12 * time.Hour), dec("500.00")},
	})
	refreshCA(t, db, "rwa_quote_prices_1d")

	cases := []struct {
		name       string
		pairID     int64
		side       prices.Side
		wantPrice  string
		wantBucket time.Time
	}{
		{"pair1 last", p1, prices.SideLast, "120.00", day2},
		{"pair1 bid", p1, prices.SideBid, "999.00", day1},
		{"pair2 last", p2, prices.SideLast, "500.00", day1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price, ts, found, err := repo.AllTimeHighLast(context.Background(), tc.pairID, string(tc.side))
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, price.Equal(dec(tc.wantPrice)), "price=%s", price)
			require.True(t, ts.Equal(tc.wantBucket), "bucket=%s, want %s", ts, tc.wantBucket)
		})
	}
}

func TestRWAPriceAtOrBefore_PicksLatestNotNearest(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	at := utcDay(2026, 6, 10).Add(12 * time.Hour)
	wantTS := at.Add(-90 * time.Minute)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{at.Add(-6 * time.Hour), dec("10.00")},
		{wantTS, dec("20.00")},
		// One minute past the anchor: nearest in absolute distance, but after it.
		{at.Add(time.Minute), dec("30.00")},
	})

	price, ts, found, err := repo.PriceAtOrBefore(context.Background(), pid, string(prices.SideLast), at)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.Equal(dec("20.00")), "at-or-before must pick the latest sample at/below the anchor; got %s", price)
	require.True(t, ts.Equal(wantTS), "ts=%s, want %s", ts, wantTS)
	require.Equal(t, time.UTC, ts.Location())
}

func TestRWAPriceAtOrBefore_InclusiveAtBoundary(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	at := utcDay(2026, 6, 11).Add(8 * time.Hour)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{at.Add(-time.Hour), dec("40.00")},
		{at, dec("42.00")},
		{at.Add(time.Second), dec("44.00")},
	})

	price, ts, found, err := repo.PriceAtOrBefore(context.Background(), pid, string(prices.SideLast), at)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.Equal(dec("42.00")), "the sample exactly at the anchor is in range; got %s", price)
	require.True(t, ts.Equal(at))
}

func TestRWAPriceAtOrBefore_BeforeAnyData_NotFound(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	first := utcDay(2026, 6, 12).Add(9 * time.Hour)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{{first, dec("55.00")}})

	price, ts, found, err := repo.PriceAtOrBefore(
		context.Background(), pid, string(prices.SideLast), first.Add(-time.Second))
	require.NoError(t, err)
	require.False(t, found)
	require.True(t, price.IsZero())
	require.True(t, ts.IsZero())
}

func TestRWAPriceAtOrBefore_IsolatedBySideAndPair(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	p1 := insertRWAPair(t, db, "mars1", "usdt")
	p2 := insertRWAPair(t, db, "mars2", "usdt")

	at := utcDay(2026, 6, 13).Add(15 * time.Hour)
	saveRWAStatTicks(t, repo, p1, prices.SideLast, []statTick{{at.Add(-2 * time.Hour), dec("11.00")}})
	saveRWAStatTicks(t, repo, p1, prices.SideBid, []statTick{{at.Add(-time.Hour), dec("22.00")}})
	saveRWAStatTicks(t, repo, p2, prices.SideLast, []statTick{{at.Add(-time.Hour), dec("33.00")}})

	cases := []struct {
		name   string
		pairID int64
		side   prices.Side
		want   string
	}{
		{"pair1 last", p1, prices.SideLast, "11.00"},
		{"pair1 bid", p1, prices.SideBid, "22.00"},
		{"pair2 last", p2, prices.SideLast, "33.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price, _, found, err := repo.PriceAtOrBefore(context.Background(), tc.pairID, string(tc.side), at)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, price.Equal(dec(tc.want)), "price=%s", price)
		})
	}
}

// --- 1d continuous aggregates: RWA ---

func TestRWAQueryCandles_1d_TwoDaysOHLC(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	day1 := utcDay(2026, 6, 15)
	day2 := day1.AddDate(0, 0, 1)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{day1.Add(9 * time.Hour), dec("100.00")},
		{day1.Add(12 * time.Hour), dec("130.00")},
		{day1.Add(15 * time.Hour), dec("90.00")},
		{day1.Add(20 * time.Hour), dec("110.00")},
		{day2.Add(1 * time.Hour), dec("200.00")},
		{day2.Add(10 * time.Hour), dec("260.00")},
		{day2.Add(14 * time.Hour), dec("180.00")},
		{day2.Add(23 * time.Hour), dec("210.00")},
	})
	refreshCA(t, db, "rwa_quote_prices_1d")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    string(prices.SideLast),
		Interval:  apiprices.Interval1d,
		From:      day1,
		To:        day2.AddDate(0, 0, 1),
	})
	require.NoError(t, err)
	require.Len(t, candles, 2)

	require.True(t, candles[0].Bucket.Equal(day1), "bucket=%s", candles[0].Bucket)
	require.True(t, candles[1].Bucket.Equal(day2), "bucket=%s", candles[1].Bucket)
	require.True(t, candles[0].Bucket.Before(candles[1].Bucket), "ASC ordering")

	require.True(t, candles[0].Open.Equal(dec("100.00")), "open=%s", candles[0].Open)
	require.True(t, candles[0].High.Equal(dec("130.00")), "high=%s", candles[0].High)
	require.True(t, candles[0].Low.Equal(dec("90.00")), "low=%s", candles[0].Low)
	require.True(t, candles[0].Close.Equal(dec("110.00")), "close=%s", candles[0].Close)
	require.Equal(t, int64(4), candles[0].Samples)

	require.True(t, candles[1].Open.Equal(dec("200.00")), "open=%s", candles[1].Open)
	require.True(t, candles[1].High.Equal(dec("260.00")), "high=%s", candles[1].High)
	require.True(t, candles[1].Low.Equal(dec("180.00")), "low=%s", candles[1].Low)
	require.True(t, candles[1].Close.Equal(dec("210.00")), "close=%s", candles[1].Close)
	require.Equal(t, int64(4), candles[1].Samples)
}

func TestRWAQueryCandles_1d_LatestModeLimit1_ReturnsNewestDay(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	day1 := utcDay(2026, 6, 17)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{day1.Add(10 * time.Hour), dec("10.00")},
		{day2.Add(10 * time.Hour), dec("20.00")},
		{day3.Add(10 * time.Hour), dec("30.00")},
	})
	refreshCA(t, db, "rwa_quote_prices_1d")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    string(prices.SideLast),
		Interval:  apiprices.Interval1d,
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)
	require.True(t, candles[0].Bucket.Equal(day3),
		"latest mode with limit must keep the newest day, got %s", candles[0].Bucket)
	require.True(t, candles[0].Close.Equal(dec("30.00")))
}

// --- 1d continuous aggregates: FA ---

func TestQueryCandles_1d_TwoDaysOHLC(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	day1 := utcDay(2026, 7, 1)
	day2 := day1.AddDate(0, 0, 1)
	saveTokenStatTicks(t, repo, []statTick{
		{day1.Add(2 * time.Hour), dec("1.00")},
		{day1.Add(8 * time.Hour), dec("1.40")},
		{day1.Add(16 * time.Hour), dec("0.80")},
		{day1.Add(22 * time.Hour), dec("1.10")},
		{day2.Add(3 * time.Hour), dec("2.00")},
		{day2.Add(9 * time.Hour), dec("2.60")},
		{day2.Add(17 * time.Hour), dec("1.80")},
		{day2.Add(21 * time.Hour), dec("2.10")},
	})
	refreshCA(t, db, "token_prices_1d")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval1d,
		From:      day1,
		To:        day2.AddDate(0, 0, 1),
	})
	require.NoError(t, err)
	require.Len(t, candles, 2)

	require.True(t, candles[0].Bucket.Equal(day1), "bucket=%s", candles[0].Bucket)
	require.True(t, candles[1].Bucket.Equal(day2), "bucket=%s", candles[1].Bucket)
	require.True(t, candles[0].Bucket.Before(candles[1].Bucket), "ASC ordering")

	require.True(t, candles[0].Open.Equal(dec("1.00")), "open=%s", candles[0].Open)
	require.True(t, candles[0].High.Equal(dec("1.40")), "high=%s", candles[0].High)
	require.True(t, candles[0].Low.Equal(dec("0.80")), "low=%s", candles[0].Low)
	require.True(t, candles[0].Close.Equal(dec("1.10")), "close=%s", candles[0].Close)
	require.Equal(t, int64(4), candles[0].Samples)

	require.True(t, candles[1].Open.Equal(dec("2.00")), "open=%s", candles[1].Open)
	require.True(t, candles[1].High.Equal(dec("2.60")), "high=%s", candles[1].High)
	require.True(t, candles[1].Low.Equal(dec("1.80")), "low=%s", candles[1].Low)
	require.True(t, candles[1].Close.Equal(dec("2.10")), "close=%s", candles[1].Close)
	require.Equal(t, int64(4), candles[1].Samples)
}

func TestQueryCandles_1d_LatestModeLimit1_ReturnsNewestDay(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	day1 := utcDay(2026, 7, 5)
	day2 := day1.AddDate(0, 0, 1)
	day3 := day1.AddDate(0, 0, 2)
	saveTokenStatTicks(t, repo, []statTick{
		{day1.Add(10 * time.Hour), dec("3.00")},
		{day2.Add(10 * time.Hour), dec("4.00")},
		{day3.Add(10 * time.Hour), dec("5.00")},
	})
	refreshCA(t, db, "token_prices_1d")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval1d,
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)
	require.True(t, candles[0].Bucket.Equal(day3),
		"latest mode with limit must keep the newest day, got %s", candles[0].Bucket)
	require.True(t, candles[0].Close.Equal(dec("5.00")))
}

func TestRWAAllTimeHighLast_RealtimeAggregation_SeesUnmaterializedHigh(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	day1 := utcDay(2026, 6, 20)
	day2 := day1.AddDate(0, 0, 1)
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{day1.Add(10 * time.Hour), dec("100.00")},
	})
	refreshCA(t, db, "rwa_quote_prices_1d")

	price, ts, found, err := repo.AllTimeHighLast(context.Background(), pid, string(prices.SideLast))
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.Equal(dec("100.00")))
	require.True(t, ts.Equal(day1))

	// Deliberately not refreshed: materialized_only=false (migration 0021) must
	// union this raw tail into the 1d view.
	saveRWAStatTicks(t, repo, pid, prices.SideLast, []statTick{
		{day2.Add(9 * time.Hour), dec("175.00")},
	})

	price, ts, found, err = repo.AllTimeHighLast(context.Background(), pid, string(prices.SideLast))
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, price.Equal(dec("175.00")),
		"real-time aggregation must surface the un-materialized daily high; got %s", price)
	require.True(t, ts.Equal(day2), "bucket=%s, want %s", ts, day2)
}
