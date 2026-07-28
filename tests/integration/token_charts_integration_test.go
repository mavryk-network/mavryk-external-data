//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
)

// fixtureSavePoints inserts ticks via the production Save() path — same
// upsert clause, same batching, same column mapping. Uses (mvrk, coingecko,
// usd) which is seeded in 0009_seed.sql.
//
// Returns the ts of the first and last point so tests can build queries
// that span the inserted window.
func fixtureSavePoints(t *testing.T, repo *repositories.TokenPriceRepository, anchor time.Time, prices_ []decimal.Decimal) (firstTS, lastTS time.Time) {
	t.Helper()
	pts := make([]prices.PricePoint, 0, len(prices_))
	for i, p := range prices_ {
		ts := anchor.Add(time.Duration(i) * time.Second)
		pts = append(pts, prices.PricePoint{
			Source:    prices.SourceCoinGecko,
			EntityKey: "mvrk",
			Timestamp: ts,
			Metric:    "usd",
			Price:     p,
		})
	}
	n, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(len(pts)), n, "Save returned unexpected RowsAffected")
	return pts[0].Timestamp, pts[len(pts)-1].Timestamp
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		t := &testing.T{}
		t.Fatalf("dec(%q): %v", s, err)
	}
	return d
}

// --- 5m re-bucket from token_prices_1m (Stage 3) ---

func TestQueryCandles_5m_RebucketFrom1m(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	// 5 minute-buckets across one 5-minute bucket. Bucket-aligned at 12:00.
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk, Metric: "usd", Price: dec("100.00")},                      // open
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(time.Minute), Metric: "usd", Price: dec("105.00")},     // high
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(2 * time.Minute), Metric: "usd", Price: dec("99.00")},  // low
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(3 * time.Minute), Metric: "usd", Price: dec("101.00")}, // mid
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(4 * time.Minute), Metric: "usd", Price: dec("103.00")}, // close
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "token_prices_1m")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval5m,
		From:      bk.Add(-time.Hour),
		To:        bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1, "5 minute-buckets should re-bucket into one 5m candle")
	c := candles[0]
	require.True(t, c.Bucket.Equal(bk))
	require.True(t, c.Open.Equal(dec("100.00")), "open=%s", c.Open)
	require.True(t, c.High.Equal(dec("105.00")), "high=%s", c.High)
	require.True(t, c.Low.Equal(dec("99.00")), "low=%s", c.Low)
	require.True(t, c.Close.Equal(dec("103.00")), "close=%s", c.Close)
	require.Equal(t, int64(5), c.Samples, "samples summed across the inner buckets")
}

// --- 15m re-bucket: many minute-buckets, ordering preserved ---

func TestQueryCandles_15m_RebucketFrom1m(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	// Two 15-minute buckets — the first holds two ticks, the second one tick.
	bk1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	bk2 := bk1.Add(15 * time.Minute)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk1, Metric: "usd", Price: dec("10.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk1.Add(10 * time.Minute), Metric: "usd", Price: dec("20.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk2.Add(5 * time.Minute), Metric: "usd", Price: dec("30.00")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "token_prices_1m")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|usd",
		Interval: apiprices.Interval15m,
		From:     bk1.Add(-time.Hour), To: bk2.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 2)
	require.True(t, candles[0].Bucket.Before(candles[1].Bucket), "ASC ordering")
	require.True(t, candles[0].Open.Equal(dec("10.00")))
	require.True(t, candles[0].Close.Equal(dec("20.00")))
	require.True(t, candles[1].Open.Equal(dec("30.00")))
	require.True(t, candles[1].Close.Equal(dec("30.00")))
}

// --- 4h re-bucket from token_prices_1h ---

func TestQueryCandles_4h_RebucketFrom1h(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	// 3 ticks across two hours that fall in the same 4h bucket starting at 12:00.
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk, Metric: "usd", Price: dec("1.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(time.Hour), Metric: "usd", Price: dec("3.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk.Add(2*time.Hour + 30*time.Minute), Metric: "usd", Price: dec("2.00")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "token_prices_1h")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|usd",
		Interval: apiprices.Interval4h,
		From:     bk.Add(-time.Hour), To: bk.Add(8 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)
	require.True(t, candles[0].Open.Equal(dec("1.00")))
	require.True(t, candles[0].High.Equal(dec("3.00")))
	require.True(t, candles[0].Low.Equal(dec("1.00")))
	require.True(t, candles[0].Close.Equal(dec("2.00")))
}

// --- 1m candle from 60 ticks ---

func TestQueryCandles_1m_OHLC(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	// 5 ticks within one minute. Open=first by ts, close=last, high=max,
	// low=min — that's the CA contract.
	bucketStart := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	prices_ := []decimal.Decimal{
		dec("100.00"), // open
		dec("102.50"), // high
		dec("99.50"),  // low
		dec("101.20"),
		dec("101.80"), // close
	}
	first, _ := fixtureSavePoints(t, repo, bucketStart, prices_)
	require.True(t, first.Before(bucketStart.Add(time.Minute)),
		"all fixture points must fall in the same minute bucket")

	refreshCA(t, db, "token_prices_1m")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval1m,
		From:      bucketStart.Add(-time.Hour),
		To:        bucketStart.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1, "1m bucket should produce 1 candle")

	c := candles[0]
	require.True(t, c.Bucket.Equal(bucketStart),
		"bucket=%s, want %s", c.Bucket, bucketStart)
	require.True(t, c.Open.Equal(dec("100.00")), "open=%s", c.Open)
	require.True(t, c.High.Equal(dec("102.50")), "high=%s", c.High)
	require.True(t, c.Low.Equal(dec("99.50")), "low=%s", c.Low)
	require.True(t, c.Close.Equal(dec("101.80")), "close=%s", c.Close)
	require.Equal(t, int64(5), c.Samples, "samples=%d", c.Samples)
	// Volume fields are nullable until Stage 4.
	require.False(t, c.VolumeBase.Valid, "VolumeBase should be null in Stage 1")
	require.False(t, c.VolumeQuote.Valid, "VolumeQuote should be null in Stage 1")
}

// --- 1h candle from ticks across 60 minutes ---

func TestQueryCandles_1h_AggregatesAcrossMinutes(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	// 4 ticks spread across one hour, one per quarter — verifies the
	// 1h CA aggregates across the 1m buckets it's built from (the CA
	// looks at raw ticks, not _1m, so this works even though _1m and
	// _1h are independent CAs).
	bucketStart := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bucketStart, Metric: "usd", Price: dec("50.00")},                       // open
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bucketStart.Add(15 * time.Minute), Metric: "usd", Price: dec("55.00")}, // high
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bucketStart.Add(30 * time.Minute), Metric: "usd", Price: dec("48.00")}, // low
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bucketStart.Add(45 * time.Minute), Metric: "usd", Price: dec("52.00")}, // close
	}
	n, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(4), n)

	refreshCA(t, db, "token_prices_1h")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval1h,
		From:      bucketStart.Add(-2 * time.Hour),
		To:        bucketStart.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)

	c := candles[0]
	require.True(t, c.Bucket.Equal(bucketStart))
	require.True(t, c.Open.Equal(dec("50.00")))
	require.True(t, c.High.Equal(dec("55.00")))
	require.True(t, c.Low.Equal(dec("48.00")))
	require.True(t, c.Close.Equal(dec("52.00")))
	require.Equal(t, int64(4), c.Samples)
}

// --- Two adjacent 1h buckets, ordering and isolation ---

func TestQueryCandles_1h_TwoBuckets_OrderASC(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	bk1 := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	bk2 := bk1.Add(time.Hour)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk1.Add(10 * time.Minute), Metric: "usd", Price: dec("10.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk1.Add(40 * time.Minute), Metric: "usd", Price: dec("12.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk2.Add(10 * time.Minute), Metric: "usd", Price: dec("13.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk2.Add(40 * time.Minute), Metric: "usd", Price: dec("11.00")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)

	refreshCA(t, db, "token_prices_1h")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk",
		AuxKey:    "coingecko|usd",
		Interval:  apiprices.Interval1h,
		From:      bk1.Add(-time.Minute),
		To:        bk2.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 2)

	require.True(t, candles[0].Bucket.Before(candles[1].Bucket),
		"candles must come back in ASC order")
	require.True(t, candles[0].Open.Equal(dec("10.00")))
	require.True(t, candles[0].Close.Equal(dec("12.00")))
	require.True(t, candles[1].Open.Equal(dec("13.00")))
	require.True(t, candles[1].Close.Equal(dec("11.00")))
}

// --- Currency / source isolation ---

func TestQueryCandles_FiltersByCurrency(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	bk := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	// Same bucket, two currencies.
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk, Metric: "usd", Price: dec("1.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: bk, Metric: "eur", Price: dec("0.85")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "token_prices_1m")

	usd, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|usd",
		Interval: apiprices.Interval1m,
		From:     bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, usd, 1)
	require.True(t, usd[0].Close.Equal(dec("1.00")))

	eur, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|eur",
		Interval: apiprices.Interval1m,
		From:     bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, eur, 1)
	require.True(t, eur[0].Close.Equal(dec("0.85")))
}

// --- Empty range returns []  ---

func TestQueryCandles_EmptyRangeReturnsZeroCandles(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|usd",
		Interval: apiprices.Interval1h,
		From:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Empty(t, candles)
}

// --- Stage 1 caveat: 5m / 15m / 4h not yet wired ---

func TestQueryCandles_RawIntervalStillRejected(t *testing.T) {
	// raw doesn't fit any CA — there's no 0-second bucket. Stage 3 added
	// 5m/15m/4h via re-bucket; raw stays a future-stage caveat.
	db := openGorm(t)
	repo := repositories.NewTokenPriceRepository(db)

	_, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "mvrk", AuxKey: "coingecko|usd",
		Interval: apiprices.IntervalRaw,
	})
	require.Error(t, err)
}

// --- AuxKey contract ---

func TestQueryCandles_BadAuxKey_400(t *testing.T) {
	db := openGorm(t)
	repo := repositories.NewTokenPriceRepository(db)

	cases := []string{"", "no-pipe", "coingecko|", "|usd"}
	for _, aux := range cases {
		_, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
			EntityKey: "mvrk", AuxKey: aux, Interval: apiprices.Interval1h,
		})
		require.Errorf(t, err, "AuxKey=%q should be rejected", aux)
	}
}
