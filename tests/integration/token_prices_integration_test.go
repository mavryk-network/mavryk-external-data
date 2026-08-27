//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
)

// FT serving-path integration tests: TokenPriceRepository.Query (window +
// latest modes) and Count against a real TimescaleDB hypertable.
//
// token_prices FKs into tokens(symbol) and sources(code), so fixtures may
// only use symbols/codes seeded by 0009_seed.sql — tokens `mvrk` / `usdt`,
// sources `coingecko` / `equiteez`. quote_currency is free-form text.

// tokenPriceRow is one fixture row: an offset from the test's anchor, a
// quote currency and a price literal.
type tokenPriceRow struct {
	offset   time.Duration
	currency string
	price    string
}

// saveTokenPriceRows writes multi-currency rows at explicit anchor offsets
// through the production Save() path.
func saveTokenPriceRows(
	t *testing.T,
	db *gorm.DB,
	token string,
	source prices.Source,
	anchor time.Time,
	rows []tokenPriceRow,
) {
	t.Helper()
	pts := make([]prices.PricePoint, 0, len(rows))
	for _, r := range rows {
		pts = append(pts, prices.PricePoint{
			Source:    source,
			EntityKey: token,
			Timestamp: anchor.Add(r.offset),
			Metric:    r.currency,
			Price:     dec(r.price),
		})
	}
	n, err := repositories.NewTokenPriceRepository(db).Save(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, int64(len(pts)), n, "Save returned unexpected RowsAffected")
}

// pointLabels renders results as "<offsetSeconds>s/<currency>" so ordering
// assertions collapse to one slice comparison.
func pointLabels(pts []prices.PricePoint, anchor time.Time) []string {
	out := make([]string, len(pts))
	for i, p := range pts {
		out[i] = fmt.Sprintf("%ds/%s", int(p.Timestamp.UTC().Sub(anchor)/time.Second), p.Metric)
	}
	return out
}

func metricsOf(pts []prices.PricePoint) []string {
	out := make([]string, len(pts))
	for i, p := range pts {
		out[i] = p.Metric
	}
	return out
}

// windowAnchor is the base timestamp for the window-mode fixtures.
var windowAnchor = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)

// seedWindowFixture lays out three timestamps × up-to-four currencies for
// (mvrk, coingecko). Insertion order is deliberately unsorted so a missing
// ORDER BY cannot pass by accident.
func seedWindowFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	saveTokenPriceRows(t, db, "mvrk", prices.SourceCoinGecko, windowAnchor, []tokenPriceRow{
		{120 * time.Second, "usd", "1.20"},
		{0, "usd", "1.00"},
		{60 * time.Second, "jpy", "150.00"},
		{0, "btc", "0.0000031"},
		{60 * time.Second, "usd", "1.10"},
		{0, "eur", "0.92"},
		{60 * time.Second, "eur", "0.93"},
		{0, "jpy", "149.00"},
	})
}

// --- WINDOW mode: ordering ---

func TestTokenPriceQuery_Window_OrdersByTsThenCurrency(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedWindowFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	got, err := repo.Query(context.Background(), prices.Query{
		Source:    prices.SourceCoinGecko,
		EntityKey: "mvrk",
		From:      windowAnchor,
		To:        windowAnchor.Add(2 * time.Minute),
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"0s/btc", "0s/eur", "0s/jpy", "0s/usd",
		"60s/eur", "60s/jpy", "60s/usd",
		"120s/usd",
	}, pointLabels(got, windowAnchor))
	require.True(t, got[0].Price.Equal(dec("0.0000031")), "price round-trip: %s", got[0].Price)
	require.Equal(t, prices.SourceCoinGecko, got[0].Source)
	require.Equal(t, "mvrk", got[0].EntityKey)
}

// --- WINDOW mode: From/To are inclusive on both ends ---

func TestTokenPriceQuery_Window_BoundsAreInclusive(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedWindowFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	tests := []struct {
		name string
		from time.Duration
		to   time.Duration
		want []string
	}{
		{
			name: "exact bounds include both edges",
			from: 0,
			to:   120 * time.Second,
			want: []string{"0s/btc", "0s/eur", "0s/jpy", "0s/usd", "60s/eur", "60s/jpy", "60s/usd", "120s/usd"},
		},
		{
			name: "single instant",
			from: 60 * time.Second,
			to:   60 * time.Second,
			want: []string{"60s/eur", "60s/jpy", "60s/usd"},
		},
		{
			name: "one second inside each edge drops both edge timestamps",
			from: time.Second,
			to:   119 * time.Second,
			want: []string{"60s/eur", "60s/jpy", "60s/usd"},
		},
		{
			name: "lower bound only",
			from: 60 * time.Second,
			to:   10 * time.Minute,
			want: []string{"60s/eur", "60s/jpy", "60s/usd", "120s/usd"},
		},
		{
			name: "window entirely after the data",
			from: 5 * time.Minute,
			to:   10 * time.Minute,
			want: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Query(context.Background(), prices.Query{
				Source:    prices.SourceCoinGecko,
				EntityKey: "mvrk",
				From:      windowAnchor.Add(tc.from),
				To:        windowAnchor.Add(tc.to),
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, pointLabels(got, windowAnchor))
		})
	}
}

// --- WINDOW mode: metric filter with multiple values ---

func TestTokenPriceQuery_Window_MetricFilter(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedWindowFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	tests := []struct {
		name    string
		metrics []string
		want    []string
	}{
		{
			name:    "three values",
			metrics: []string{"usd", "btc", "eur"},
			want:    []string{"0s/btc", "0s/eur", "0s/usd", "60s/eur", "60s/usd", "120s/usd"},
		},
		{
			name:    "single value",
			metrics: []string{"jpy"},
			want:    []string{"0s/jpy", "60s/jpy"},
		},
		{
			name:    "unknown currency matches nothing",
			metrics: []string{"krw"},
			want:    []string{},
		},
		{
			name:    "no filter returns every currency",
			metrics: nil,
			want:    []string{"0s/btc", "0s/eur", "0s/jpy", "0s/usd", "60s/eur", "60s/jpy", "60s/usd", "120s/usd"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Query(context.Background(), prices.Query{
				Source:    prices.SourceCoinGecko,
				EntityKey: "mvrk",
				Metrics:   tc.metrics,
				From:      windowAnchor,
				To:        windowAnchor.Add(2 * time.Minute),
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, pointLabels(got, windowAnchor))
		})
	}
}

// --- WINDOW mode: Limit truncates the ordered result ---

func TestTokenPriceQuery_Window_Limit(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedWindowFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	for _, limit := range []int{1, 3, 8, 50} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			got, err := repo.Query(context.Background(), prices.Query{
				Source:    prices.SourceCoinGecko,
				EntityKey: "mvrk",
				From:      windowAnchor,
				To:        windowAnchor.Add(2 * time.Minute),
				Limit:     limit,
			})
			require.NoError(t, err)
			all := []string{"0s/btc", "0s/eur", "0s/jpy", "0s/usd", "60s/eur", "60s/jpy", "60s/usd", "120s/usd"}
			if limit > len(all) {
				limit = len(all)
			}
			require.Equal(t, all[:limit], pointLabels(got, windowAnchor))
		})
	}
}

func TestTokenPriceQuery_Window_LimitCombinesWithMetricFilter(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedWindowFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	got, err := repo.Query(context.Background(), prices.Query{
		Source:    prices.SourceCoinGecko,
		EntityKey: "mvrk",
		Metrics:   []string{"usd", "eur"},
		From:      windowAnchor,
		To:        windowAnchor.Add(2 * time.Minute),
		Limit:     3,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"0s/eur", "0s/usd", "60s/eur"}, pointLabels(got, windowAnchor))
}

// --- WINDOW mode: isolation by token and by source ---

func TestTokenPriceQuery_Window_IsolatedByTokenAndSource(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)

	saveTokenPriceRows(t, db, "mvrk", prices.SourceCoinGecko, windowAnchor, []tokenPriceRow{
		{0, "usd", "1.00"},
		{60 * time.Second, "usd", "1.10"},
	})
	saveTokenPriceRows(t, db, "usdt", prices.SourceCoinGecko, windowAnchor, []tokenPriceRow{
		{0, "usd", "0.9990"},
	})
	saveTokenPriceRows(t, db, "mvrk", prices.SourceEquiteez, windowAnchor, []tokenPriceRow{
		{0, "usd", "42.00"},
		{60 * time.Second, "usd", "43.00"},
		{120 * time.Second, "usd", "44.00"},
	})
	repo := repositories.NewTokenPriceRepository(db)

	window := func(token string, source prices.Source) []prices.PricePoint {
		got, err := repo.Query(context.Background(), prices.Query{
			Source:    source,
			EntityKey: token,
			From:      windowAnchor,
			To:        windowAnchor.Add(10 * time.Minute),
		})
		require.NoError(t, err)
		return got
	}

	mvrkCG := window("mvrk", prices.SourceCoinGecko)
	require.Len(t, mvrkCG, 2)
	require.True(t, mvrkCG[0].Price.Equal(dec("1.00")))
	require.True(t, mvrkCG[1].Price.Equal(dec("1.10")))

	usdtCG := window("usdt", prices.SourceCoinGecko)
	require.Len(t, usdtCG, 1)
	require.True(t, usdtCG[0].Price.Equal(dec("0.9990")))

	mvrkEQ := window("mvrk", prices.SourceEquiteez)
	require.Len(t, mvrkEQ, 3)
	require.Equal(t, prices.SourceEquiteez, mvrkEQ[0].Source)

	require.Empty(t, window("usdt", prices.SourceEquiteez))
}

// --- LATEST mode ---

// latestAnchor is "now" for the latest-mode fixtures; rows are placed at
// negative offsets from it.
var latestAnchor = time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)

// seedLatestFixture writes several samples per currency with staggered
// timestamps so DISTINCT ON has something to collapse, and so the newest
// row per currency is not the newest row overall.
func seedLatestFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	saveTokenPriceRows(t, db, "mvrk", prices.SourceCoinGecko, latestAnchor, []tokenPriceRow{
		{-9 * time.Minute, "usd", "1.00"},
		{-5 * time.Minute, "usd", "1.05"},
		{-1 * time.Minute, "usd", "1.09"},
		{-9 * time.Minute, "eur", "0.90"},
		{-2 * time.Minute, "eur", "0.95"},
		{-8 * time.Minute, "btc", "0.0000030"},
		{-3 * time.Minute, "btc", "0.0000033"},
		{-7 * time.Minute, "jpy", "148.00"},
		{-6 * time.Minute, "jpy", "151.00"},
	})
}

func TestTokenPriceQuery_Latest_DistinctOnPerCurrency(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedLatestFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}
	require.True(t, q.IsLatest(), "zero From/To must route through the DISTINCT ON path")

	got, err := repo.Query(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, []string{"btc", "eur", "jpy", "usd"}, metricsOf(got))

	want := map[string]struct {
		price  string
		offset time.Duration
	}{
		"btc": {"0.0000033", -3 * time.Minute},
		"eur": {"0.95", -2 * time.Minute},
		"jpy": {"151.00", -6 * time.Minute},
		"usd": {"1.09", -1 * time.Minute},
	}
	for _, p := range got {
		w := want[p.Metric]
		require.True(t, p.Price.Equal(dec(w.price)), "%s: got %s want %s", p.Metric, p.Price, w.price)
		require.True(t, p.Timestamp.UTC().Equal(latestAnchor.Add(w.offset)),
			"%s: got ts %s", p.Metric, p.Timestamp.UTC())
		require.Equal(t, prices.SourceCoinGecko, p.Source)
		require.Equal(t, "mvrk", p.EntityKey)
	}
}

// The multi-value case is the regression guard for metricFilterFragment:
// binding q.Metrics as a Go slice in the raw SQL fails under pgx with
// "malformed array literal".
func TestTokenPriceQuery_Latest_MetricFilter(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedLatestFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	tests := []struct {
		name       string
		metrics    []string
		wantMetric []string
	}{
		{name: "three values", metrics: []string{"usd", "btc", "eur"}, wantMetric: []string{"btc", "eur", "usd"}},
		{name: "two values", metrics: []string{"jpy", "usd"}, wantMetric: []string{"jpy", "usd"}},
		{name: "one value", metrics: []string{"eur"}, wantMetric: []string{"eur"}},
		{name: "zero values", metrics: nil, wantMetric: []string{"btc", "eur", "jpy", "usd"}},
		{name: "empty non-nil slice", metrics: []string{}, wantMetric: []string{"btc", "eur", "jpy", "usd"}},
		{name: "unknown currency", metrics: []string{"krw"}, wantMetric: []string{}},
		{name: "duplicate values", metrics: []string{"usd", "usd"}, wantMetric: []string{"usd"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Query(context.Background(), prices.Query{
				Source:    prices.SourceCoinGecko,
				EntityKey: "mvrk",
				Metrics:   tc.metrics,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantMetric, metricsOf(got))
		})
	}
}

// Limit runs after deduplication: with 9 rows collapsing to 4 currencies,
// Limit=2 must yield the 2 freshest-per-currency rows, not the first 2 raw
// rows the scan happens to hit.
func TestTokenPriceQuery_Latest_LimitAppliedAfterDedup(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedLatestFixture(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	tests := []struct {
		limit int
		want  []string
	}{
		{limit: 1, want: []string{"btc"}},
		{limit: 2, want: []string{"btc", "eur"}},
		{limit: 4, want: []string{"btc", "eur", "jpy", "usd"}},
		{limit: 9, want: []string{"btc", "eur", "jpy", "usd"}},
		{limit: 0, want: []string{"btc", "eur", "jpy", "usd"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("limit=%d", tc.limit), func(t *testing.T) {
			got, err := repo.Query(context.Background(), prices.Query{
				Source:    prices.SourceCoinGecko,
				EntityKey: "mvrk",
				Limit:     tc.limit,
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, metricsOf(got))
			freshest := map[string]string{"btc": "0.0000033", "eur": "0.95", "jpy": "151.00", "usd": "1.09"}
			for _, p := range got {
				require.True(t, p.Price.Equal(dec(freshest[p.Metric])),
					"%s kept a stale sample: %s", p.Metric, p.Price)
			}
		})
	}
}

func TestTokenPriceQuery_Latest_IsolatedByTokenAndSource(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedLatestFixture(t, db)
	saveTokenPriceRows(t, db, "mvrk", prices.SourceEquiteez, latestAnchor, []tokenPriceRow{
		{-4 * time.Minute, "usd", "42.00"},
		{-30 * time.Second, "usd", "44.00"},
	})
	saveTokenPriceRows(t, db, "usdt", prices.SourceCoinGecko, latestAnchor, []tokenPriceRow{
		{-30 * time.Second, "usd", "0.9990"},
	})
	repo := repositories.NewTokenPriceRepository(db)

	mvrkEQ, err := repo.Query(context.Background(), prices.Query{
		Source: prices.SourceEquiteez, EntityKey: "mvrk",
	})
	require.NoError(t, err)
	require.Len(t, mvrkEQ, 1)
	require.True(t, mvrkEQ[0].Price.Equal(dec("44.00")))
	require.Equal(t, prices.SourceEquiteez, mvrkEQ[0].Source)

	usdtCG, err := repo.Query(context.Background(), prices.Query{
		Source: prices.SourceCoinGecko, EntityKey: "usdt",
	})
	require.NoError(t, err)
	require.Len(t, usdtCG, 1)
	require.True(t, usdtCG[0].Price.Equal(dec("0.9990")))

	mvrkCG, err := repo.Query(context.Background(), prices.Query{
		Source: prices.SourceCoinGecko, EntityKey: "mvrk",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"btc", "eur", "jpy", "usd"}, metricsOf(mvrkCG))
	require.True(t, mvrkCG[3].Price.Equal(dec("1.09")), "coingecko usd must not pick up the equiteez row")
}

func TestTokenPriceQuery_Latest_EmptyTable(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	got, err := repo.Query(context.Background(), prices.Query{
		Source: prices.SourceCoinGecko, EntityKey: "mvrk", Metrics: []string{"usd", "eur", "btc"},
	})
	require.NoError(t, err)
	require.Empty(t, got)
}

// --- Count ---

func TestTokenPriceCount(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	seedLatestFixture(t, db)
	saveTokenPriceRows(t, db, "mvrk", prices.SourceEquiteez, latestAnchor, []tokenPriceRow{
		{-4 * time.Minute, "usd", "42.00"},
		{-30 * time.Second, "usd", "44.00"},
	})
	repo := repositories.NewTokenPriceRepository(db)

	tests := []struct {
		name   string
		source prices.Source
		token  string
		want   int64
	}{
		{name: "counts every metric for the token", source: prices.SourceCoinGecko, token: "mvrk", want: 9},
		{name: "isolated per source", source: prices.SourceEquiteez, token: "mvrk", want: 2},
		{name: "empty token", source: prices.SourceCoinGecko, token: "usdt", want: 0},
		{name: "unseeded token", source: prices.SourceCoinGecko, token: "nosuchtoken", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, err := repo.Count(context.Background(), tc.source, tc.token)
			require.NoError(t, err)
			require.Equal(t, tc.want, n)
		})
	}
}

func TestTokenPriceCount_EmptyTable(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	n, err := repo.Count(context.Background(), prices.SourceCoinGecko, "mvrk")
	require.NoError(t, err)
	require.Zero(t, n)
}
