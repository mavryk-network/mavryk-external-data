//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
)

// insertRWAPair seeds an `rwa_pairs` row referencing the seeded `equiteez`
// source (0009_seed.sql). Returns the assigned bigserial id, used to build
// the `rwa_quote_prices.pair_id` FK and CandleQuery.EntityKey.
//
// No ON CONFLICT path: the natural-key index on (source_code, orderbook_addr)
// is partial (WHERE orderbook_addr IS NOT NULL), which Postgres rejects as
// a target for ON CONFLICT inference. truncateRWA below resets the slate
// before each test instead.
func insertRWAPair(t *testing.T, db *gorm.DB, base, quote string) int64 {
	t.Helper()
	var id int64
	addr := "KT1test_" + base + "_" + quote
	err := db.Raw(`
		INSERT INTO rwa_pairs (base_symbol, quote_symbol, source_code, orderbook_addr, enabled)
		VALUES (?, ?, 'equiteez', ?, true)
		RETURNING id
	`, base, quote, addr).Row().Scan(&id)
	require.NoError(t, err)
	require.NotZero(t, id, "rwa_pairs.id is zero")
	return id
}

// truncateRWA wipes both the lookup rows and the price ticks, then resets
// the bigserial counter. CASCADE drops the FK rows in rwa_quote_prices in
// the same statement; RESTART IDENTITY keeps pair_ids deterministic across
// tests within a run (handy for debugging, not load-bearing).
func truncateRWA(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(
		`TRUNCATE TABLE rwa_pairs, rwa_quote_prices RESTART IDENTITY CASCADE`,
	).Error)
}

// fixtureSaveRWAPoints inserts ticks via the production Save path — same
// upsert clause, same pair_id parsing — so the test exercises the column
// mapping the live collector uses.
func fixtureSaveRWAPoints(
	t *testing.T,
	repo *repositories.RWAPriceRepository,
	pairID int64,
	side prices.Side,
	anchor time.Time,
	values []decimal.Decimal,
) {
	t.Helper()
	pts := make([]prices.PricePoint, 0, len(values))
	pidStr := strconv.FormatInt(pairID, 10)
	for i, v := range values {
		pts = append(pts, prices.PricePoint{
			Source:    prices.SourceEquiteez,
			EntityKey: pidStr,
			Timestamp: anchor.Add(time.Duration(i) * time.Second),
			Metric:    string(side),
			Price:     v,
		})
	}
	n, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(len(pts)), n)
}

// --- 5m re-bucket from rwa_quote_prices_1m (Stage 3) ---

func TestRWAQueryCandles_5m_RebucketFrom1m(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pidStr := strconv.FormatInt(pid, 10)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk, Metric: "last", Price: dec("100.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(time.Minute), Metric: "last", Price: dec("105.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(2 * time.Minute), Metric: "last", Price: dec("99.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(3 * time.Minute), Metric: "last", Price: dec("101.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(4 * time.Minute), Metric: "last", Price: dec("103.00")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "rwa_quote_prices_1m")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: pidStr, AuxKey: "last",
		Interval: apiprices.Interval5m,
		From:     bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)
	c := candles[0]
	require.True(t, c.Bucket.Equal(bk))
	require.True(t, c.Open.Equal(dec("100.00")))
	require.True(t, c.High.Equal(dec("105.00")))
	require.True(t, c.Low.Equal(dec("99.00")))
	require.True(t, c.Close.Equal(dec("103.00")))
	require.Equal(t, int64(5), c.Samples)
}

func TestRWAQueryCandles_4h_RebucketFrom1h(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pidStr := strconv.FormatInt(pid, 10)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk, Metric: "last", Price: dec("1.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(time.Hour), Metric: "last", Price: dec("3.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: bk.Add(2*time.Hour + 30*time.Minute), Metric: "last", Price: dec("2.00")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "rwa_quote_prices_1h")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: pidStr, AuxKey: "last",
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

// --- 1m candle from a few ticks in one minute ---

func TestRWAQueryCandles_1m_OHLC(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	values := []decimal.Decimal{
		dec("100.00"), // open
		dec("105.50"), // high
		dec("99.00"),  // low
		dec("102.00"),
		dec("103.30"), // close
	}
	fixtureSaveRWAPoints(t, repo, pid, prices.SideLast, bk, values)
	refreshCA(t, db, "rwa_quote_prices_1m")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    string(prices.SideLast),
		Interval:  apiprices.Interval1m,
		From:      bk.Add(-time.Hour),
		To:        bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)

	c := candles[0]
	require.True(t, c.Bucket.Equal(bk))
	require.True(t, c.Open.Equal(dec("100.00")), "open=%s", c.Open)
	require.True(t, c.High.Equal(dec("105.50")), "high=%s", c.High)
	require.True(t, c.Low.Equal(dec("99.00")), "low=%s", c.Low)
	require.True(t, c.Close.Equal(dec("103.30")), "close=%s", c.Close)
	require.Equal(t, int64(5), c.Samples)
	// Volume parked until Stage 4.
	require.False(t, c.VolumeBase.Valid)
	require.False(t, c.VolumeQuote.Valid)
}

// --- 1h candle aggregating across the hour ---

func TestRWAQueryCandles_1h_AggregatesAcrossMinutes(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	bk := time.Date(2026, 5, 1, 13, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk, Metric: "last", Price: dec("50.00")},                       // open
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk.Add(15 * time.Minute), Metric: "last", Price: dec("55.00")}, // high
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk.Add(30 * time.Minute), Metric: "last", Price: dec("48.00")}, // low
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk.Add(45 * time.Minute), Metric: "last", Price: dec("52.00")}, // close
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)

	refreshCA(t, db, "rwa_quote_prices_1h")

	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    string(prices.SideLast),
		Interval:  apiprices.Interval1h,
		From:      bk.Add(-2 * time.Hour),
		To:        bk.Add(2 * time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, candles, 1)

	c := candles[0]
	require.True(t, c.Open.Equal(dec("50.00")))
	require.True(t, c.High.Equal(dec("55.00")))
	require.True(t, c.Low.Equal(dec("48.00")))
	require.True(t, c.Close.Equal(dec("52.00")))
	require.Equal(t, int64(4), c.Samples)
}

// --- side isolation: bid and last in the same bucket are independent rows ---

func TestRWAQueryCandles_FiltersBySide(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")

	bk := time.Date(2026, 5, 1, 14, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk, Metric: "last", Price: dec("100.00")},
		{Source: prices.SourceEquiteez, EntityKey: strconv.FormatInt(pid, 10), Timestamp: bk, Metric: "bid", Price: dec("99.50")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)
	refreshCA(t, db, "rwa_quote_prices_1m")

	last, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    "last",
		Interval:  apiprices.Interval1m,
		From:      bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, last, 1)
	require.True(t, last[0].Close.Equal(dec("100.00")))

	bid, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10),
		AuxKey:    "bid",
		Interval:  apiprices.Interval1m,
		From:      bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, bid, 1)
	require.True(t, bid[0].Close.Equal(dec("99.50")))
}

// --- pair isolation: two pairs in the same bucket don't bleed ---

func TestRWAQueryCandles_FiltersByPair(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	p1 := insertRWAPair(t, db, "mars1", "usdt")
	p2 := insertRWAPair(t, db, "mars2", "usdt")

	bk := time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC)
	fixtureSaveRWAPoints(t, repo, p1, prices.SideLast, bk, []decimal.Decimal{dec("10.00")})
	fixtureSaveRWAPoints(t, repo, p2, prices.SideLast, bk, []decimal.Decimal{dec("20.00")})
	refreshCA(t, db, "rwa_quote_prices_1m")

	c1, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(p1, 10), AuxKey: "last",
		Interval: apiprices.Interval1m,
		From:     bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, c1, 1)
	require.True(t, c1[0].Close.Equal(dec("10.00")))

	c2, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(p2, 10), AuxKey: "last",
		Interval: apiprices.Interval1m,
		From:     bk.Add(-time.Hour), To: bk.Add(time.Hour),
	})
	require.NoError(t, err)
	require.Len(t, c2, 1)
	require.True(t, c2[0].Close.Equal(dec("20.00")))
}

// --- Stage-3 caveats and AuxKey contract ---

func TestRWAQueryCandles_RawIntervalStillRejected(t *testing.T) {
	// Stage 3 added 5m/15m/4h via re-bucket; raw stays a future-stage caveat.
	db := openGorm(t)
	repo := repositories.NewRWAPriceRepository(db)
	_, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "1", AuxKey: "last", Interval: apiprices.IntervalRaw,
	})
	require.Error(t, err)
}

func TestRWAQueryCandles_BadAuxKeyOrEntityKey_400(t *testing.T) {
	db := openGorm(t)
	repo := repositories.NewRWAPriceRepository(db)

	// Empty AuxKey
	_, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "1", AuxKey: "", Interval: apiprices.Interval1h,
	})
	require.Error(t, err)

	// Non-numeric EntityKey
	_, err = repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: "not-a-number", AuxKey: "last", Interval: apiprices.Interval1h,
	})
	require.Error(t, err)
}

// --- Empty range → no rows, not 404 ---

func TestRWAQueryCandles_EmptyRangeReturnsZeroCandles(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	repo := repositories.NewRWAPriceRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	candles, err := repo.QueryCandles(context.Background(), apiprices.CandleQuery{
		EntityKey: strconv.FormatInt(pid, 10), AuxKey: "last",
		Interval: apiprices.Interval1h,
		From:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Empty(t, candles)
}
