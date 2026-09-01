//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"quotes/internal/core/infrastructure/storage/repositories"
)

// Mirrors the unexported defaultLegacyRowCap in legacy_quotes_repository.go.
const legacyRowCap = 10000

// Raw SQL rather than TokenPriceRepository.Save, so the numeric literal reaches
// the numeric(38,18) column without a Go decimal round-trip in between.
func legacyInsertQuote(t *testing.T, db *gorm.DB, token, source string, ts time.Time, byCurrency map[string]string) {
	t.Helper()
	for currency, price := range byCurrency {
		require.NoError(t, db.Exec(
			`INSERT INTO token_prices (token_symbol, source_code, ts, quote_currency, price)
			 VALUES (?, ?, ?, ?, CAST(? AS numeric(38,18)))`,
			token, source, ts.UTC(), currency, price).Error)
	}
}

// n rows one minute apart, one currency each, so the pivot sees exactly n
// distinct timestamps. INSERT..SELECT keeps an over-the-row-cap fixture cheap.
func legacyInsertSeries(t *testing.T, db *gorm.DB, token, source string, start time.Time, n int) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO token_prices (token_symbol, source_code, ts, quote_currency, price)
		 SELECT ?, ?, ?::timestamptz + (g * INTERVAL '1 minute'), 'usd', g
		 FROM generate_series(0, ?) AS g`,
		token, source, start.UTC(), n-1).Error)
}

// Two of the pivot's contracts are invisible from the returned struct — a
// dropped COALESCE (gorm decodes NULL as 0) and a missing LIMIT — but both show
// up in the SQL gorm emits.
type legacySQLRecorder struct {
	statements []string
}

func (r *legacySQLRecorder) LogMode(logger.LogLevel) logger.Interface      { return r }
func (r *legacySQLRecorder) Info(context.Context, string, ...interface{})  {}
func (r *legacySQLRecorder) Warn(context.Context, string, ...interface{})  {}
func (r *legacySQLRecorder) Error(context.Context, string, ...interface{}) {}

func (r *legacySQLRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	stmt, _ := fc()
	r.statements = append(r.statements, stmt)
}

func (r *legacySQLRecorder) last(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, r.statements, "gorm emitted no SQL")
	return r.statements[len(r.statements)-1]
}

// Its own connection, so the recorder only sees the call under test.
func legacyRecordingRepo(t *testing.T, rec *legacySQLRecorder) *repositories.LegacyQuoteRepository {
	t.Helper()
	require.NotEmpty(t, pgDSN, "pgDSN unset — TestMain didn't run; missing build tag?")

	db, err := gorm.Open(postgres.Open(pgDSN), &gorm.Config{Logger: rec})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return repositories.NewLegacyQuoteRepository(db)
}

func legacyQuoteTimestamps(rows []repositories.LegacyQuoteRow) []time.Time {
	out := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Timestamp.UTC())
	}
	return out
}

func TestLegacyQueryWide_FillsAllEightCurrencies(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	ts := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	legacyInsertQuote(t, db, "mvrk", "coingecko", ts, map[string]string{
		"btc": "0.000012",
		"usd": "1.23",
		"eur": "1.10",
		"cny": "8.90",
		"jpy": "185.5",
		"krw": "1650.25",
		"eth": "0.00035",
		"gbp": "0.97",
	})

	rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko",
		ts.Add(-time.Hour), ts.Add(time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1, "eight long rows must collapse into one wide row")

	got := rows[0]
	require.True(t, got.Timestamp.Equal(ts), "ts=%s, want %s", got.Timestamp, ts)
	require.InDelta(t, 0.000012, got.BTC, 1e-15)
	require.InDelta(t, 1.23, got.USD, 1e-12)
	require.InDelta(t, 1.10, got.EUR, 1e-12)
	require.InDelta(t, 8.90, got.CNY, 1e-12)
	require.InDelta(t, 185.5, got.JPY, 1e-12)
	require.InDelta(t, 1650.25, got.KRW, 1e-12)
	require.InDelta(t, 0.00035, got.ETH, 1e-15)
	require.InDelta(t, 0.97, got.GBP, 1e-12)
}

func TestLegacyQueryWide_MissingCurrencyIsZeroNotNull(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)

	ts := time.Date(2026, 5, 2, 8, 30, 0, 0, time.UTC)
	legacyInsertQuote(t, db, "mvrk", "coingecko", ts, map[string]string{
		"usd": "2.50",
		"btc": "0.00004",
	})

	rec := &legacySQLRecorder{}
	repo := legacyRecordingRepo(t, rec)

	rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko",
		ts.Add(-time.Hour), ts.Add(time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	got := rows[0]
	require.InDelta(t, 2.50, got.USD, 1e-12)
	require.InDelta(t, 0.00004, got.BTC, 1e-15)
	missing := map[string]float64{
		"eur": got.EUR, "cny": got.CNY, "jpy": got.JPY,
		"krw": got.KRW, "eth": got.ETH, "gbp": got.GBP,
	}
	for name, value := range missing {
		require.Equal(t, 0.0, value, "%s must decode as zero", name)
	}

	// The struct cannot tell COALESCE from its absence — gorm decodes NULL as 0
	// either way — so re-read the emitted statement's columns as nullable.
	pivot := rec.last(t)
	for name := range missing {
		var value sql.NullFloat64
		require.NoError(t,
			db.Raw(`SELECT `+name+` FROM (`+pivot+`) AS pivot`).Row().Scan(&value),
			"probing %s in the emitted pivot", name)
		require.Truef(t, value.Valid,
			"%s came back NULL — the COALESCE that preserves v0.1.0 wire behaviour is gone", name)
		require.Zero(t, value.Float64)
	}
}

func TestLegacyQueryWide_OneRowPerTimestampAscending(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	base := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(10 * time.Minute)
	t3 := base.Add(20 * time.Minute)

	// Newest-first, so the ordering can only come from ORDER BY.
	legacyInsertQuote(t, db, "mvrk", "coingecko", t3, map[string]string{"usd": "3", "eur": "3.3", "btc": "0.3"})
	legacyInsertQuote(t, db, "mvrk", "coingecko", t2, map[string]string{"usd": "2", "eur": "2.2", "btc": "0.2"})
	legacyInsertQuote(t, db, "mvrk", "coingecko", t1, map[string]string{"usd": "1", "eur": "1.1", "btc": "0.1"})

	rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko",
		t1.Add(-time.Hour), t3.Add(time.Hour), 100)
	require.NoError(t, err)
	require.Equal(t, []time.Time{t1, t2, t3}, legacyQuoteTimestamps(rows))
	require.InDelta(t, 1.0, rows[0].USD, 1e-12)
	require.InDelta(t, 2.0, rows[1].USD, 1e-12)
	require.InDelta(t, 3.0, rows[2].USD, 1e-12)
}

func TestLegacyQueryWide_WindowBoundsAreInclusive(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	base := time.Date(2026, 5, 4, 6, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(time.Minute)
	t3 := base.Add(2 * time.Minute)
	for i, ts := range []time.Time{t1, t2, t3} {
		legacyInsertQuote(t, db, "mvrk", "coingecko", ts,
			map[string]string{"usd": strconv.Itoa(i + 1)})
	}

	tests := []struct {
		name     string
		from, to time.Time
		want     []time.Time
	}{
		{"both endpoints included", t1, t3, []time.Time{t1, t2, t3}},
		{"single instant window", t2, t2, []time.Time{t2}},
		{"one microsecond inside each edge", t1.Add(time.Microsecond), t3.Add(-time.Microsecond), []time.Time{t2}},
		{"window entirely before data", t1.Add(-time.Hour), t1.Add(-time.Minute), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko", tc.from, tc.to, 100)
			require.NoError(t, err)
			if tc.want == nil {
				require.Empty(t, rows)
				return
			}
			require.Equal(t, tc.want, legacyQuoteTimestamps(rows))
		})
	}
}

// Truncation keeps the OLDEST end, as v0.1.0 did: keeping the recent end would
// make each next page jump to the window end and skip everything between.
func TestLegacyQueryWide_LimitKeepsOldestRows(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	start := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	legacyInsertSeries(t, db, "mvrk", "coingecko", start, 5)
	from, to := start.Add(-time.Hour), start.Add(time.Hour)

	tests := []struct {
		name  string
		limit int
		want  []time.Time
	}{
		{"limit below row count", 2, []time.Time{start, start.Add(time.Minute)}},
		{"limit of one", 1, []time.Time{start}},
		{"limit above row count", 50, []time.Time{
			start,
			start.Add(time.Minute),
			start.Add(2 * time.Minute),
			start.Add(3 * time.Minute),
			start.Add(4 * time.Minute),
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko", from, to, tc.limit)
			require.NoError(t, err)
			require.Equal(t, tc.want, legacyQuoteTimestamps(rows))
		})
	}
}

// Walks the window in limit-sized pages the way a v0.1.0 client does, re-issuing
// with from=<last ts seen>: the concatenation must reproduce the series exactly.
func TestLegacyQueryWide_ForwardPaginationCoversWindow(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	start := time.Date(2026, 5, 7, 8, 0, 0, 0, time.UTC)
	const total = 5
	legacyInsertSeries(t, db, "mvrk", "coingecko", start, total)
	to := start.Add(time.Hour)

	var walked []time.Time
	from := start.Add(-time.Hour)
	for page := 0; page < total; page++ {
		rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko", from, to, 2)
		require.NoError(t, err)
		if len(rows) == 0 {
			break
		}
		walked = append(walked, legacyQuoteTimestamps(rows)...)
		// Bounds are inclusive, so step one microsecond past the last row seen.
		from = rows[len(rows)-1].Timestamp.Add(time.Microsecond)
	}

	want := make([]time.Time, 0, total)
	for i := 0; i < total; i++ {
		want = append(want, start.Add(time.Duration(i)*time.Minute))
	}
	require.Equal(t, want, walked, "paging must cover the window contiguously, skipping nothing")
}

// limit <= 0 must fall back to defaultLegacyRowCap. Telling that apart from "no
// cap" needs more timestamps than the cap, hence the generate_series fixture.
func TestLegacyQueryWide_NonPositiveLimitFallsBackToRowCap(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)

	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	legacyInsertSeries(t, db, "mvrk", "coingecko", start, legacyRowCap+1)

	var total int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM token_prices`).Scan(&total).Error)
	require.Equal(t, int64(legacyRowCap+1), total,
		"fixture must exceed the row cap for this test to mean anything")

	rec := &legacySQLRecorder{}
	repo := legacyRecordingRepo(t, rec)

	from := start.Add(-time.Hour)
	to := start.Add(time.Duration(legacyRowCap+10) * time.Minute)

	for _, limit := range []int{0, -1} {
		rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko", from, to, limit)
		require.NoError(t, err)
		// len() rather than require.Len: a failure would dump 10001 rows.
		require.Equalf(t, legacyRowCap, len(rows),
			"limit=%d must fall back to the row cap, not run unbounded", limit)
		require.True(t, rows[0].Timestamp.Equal(start),
			"the cap must keep the window's oldest end so forward paging can advance")
		require.True(t, rows[len(rows)-1].Timestamp.After(rows[0].Timestamp),
			"rows stay ts ASC on the wire")
		var newest time.Time
		require.NoError(t, db.Raw(`SELECT max(ts) FROM token_prices`).Scan(&newest).Error)
		require.False(t, rows[len(rows)-1].Timestamp.Equal(newest.UTC()),
			"fixture exceeds the cap by one row, so the newest timestamp is the one dropped")
		require.Containsf(t, strings.ToUpper(rec.last(t)), "LIMIT "+strconv.Itoa(legacyRowCap),
			"limit=%d must still emit a LIMIT — the pivot is never issued unbounded", limit)
	}
}

func TestLegacyQueryWide_IsolatesByTokenAndSource(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	ts := time.Date(2026, 5, 6, 15, 0, 0, 0, time.UTC)
	legacyInsertQuote(t, db, "mvrk", "coingecko", ts, map[string]string{"usd": "1.11"})
	legacyInsertQuote(t, db, "mvrk", "equiteez", ts, map[string]string{"usd": "2.22"})
	legacyInsertQuote(t, db, "usdt", "coingecko", ts, map[string]string{"usd": "3.33"})

	from, to := ts.Add(-time.Hour), ts.Add(time.Hour)

	tests := []struct {
		name    string
		token   string
		source  string
		wantUSD float64
	}{
		{"mvrk on coingecko", "mvrk", "coingecko", 1.11},
		{"mvrk on equiteez", "mvrk", "equiteez", 2.22},
		{"usdt on coingecko", "usdt", "coingecko", 3.33},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := repo.QueryWide(context.Background(), tc.token, tc.source, from, to, 100)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.InDelta(t, tc.wantUSD, rows[0].USD, 1e-12)
		})
	}

	t.Run("unknown token", func(t *testing.T) {
		rows, err := repo.QueryWide(context.Background(), "nosuchtoken", "coingecko", from, to, 100)
		require.NoError(t, err)
		require.Empty(t, rows)
	})

	t.Run("unknown source", func(t *testing.T) {
		rows, err := repo.QueryWide(context.Background(), "mvrk", "nosuchsource", from, to, 100)
		require.NoError(t, err)
		require.Empty(t, rows)
	})
}

func TestLegacyQueryWide_NumericPrecisionSurvivesFloatCast(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewLegacyQuoteRepository(db)

	ts := time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC)
	legacyInsertQuote(t, db, "mvrk", "coingecko", ts, map[string]string{
		"usd": "1.234567890123456789",
		"btc": "0.000000000000000123",
		"krw": "1650.250000000000000000",
	})

	rows, err := repo.QueryWide(context.Background(), "mvrk", "coingecko",
		ts.Add(-time.Hour), ts.Add(time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	got := rows[0]
	// float64 holds ~15-17 digits, so a 19-digit numeric compares relatively.
	require.InEpsilon(t, 1.234567890123456789, got.USD, 1e-15)
	require.InEpsilon(t, 0.000000000000000123, got.BTC, 1e-12)
	require.Equal(t, 1650.25, got.KRW, "trailing numeric scale must not perturb an exactly representable value")
}
