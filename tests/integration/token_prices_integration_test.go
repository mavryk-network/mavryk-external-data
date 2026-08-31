//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// LatestCommonTimestamp anchors the live catch-up window. It must answer with
// the LAGGIEST currency's newest point, not the freshest overall: the live
// fetch saves partial results, so one currency failing while the others keep
// saving must still pull the window back over the resulting hole.
func TestTokenPriceLatestCommonTimestamp(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	anchor := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	// usd/eur are current; btc stopped saving two hours ago.
	saveTokenPriceRows(t, db, "mvrk", prices.SourceCoinGecko, anchor, []tokenPriceRow{
		{-2 * time.Hour, "usd", "1.00"},
		{-2 * time.Hour, "eur", "0.90"},
		{-2 * time.Hour, "btc", "0.00001"},
		{-time.Minute, "usd", "1.01"},
		{-time.Minute, "eur", "0.91"},
	})
	repo := repositories.NewTokenPriceRepository(db)
	ctx := context.Background()
	// A horizon old enough to keep every fixture row in play.
	horizon := anchor.Add(-24 * time.Hour)

	t.Run("anchors on the lagging currency", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk",
			[]string{"usd", "eur", "btc"}, horizon)
		require.NoError(t, err)
		require.True(t, got.Found)
		require.True(t, got.TS.Equal(anchor.Add(-2*time.Hour)),
			"want btc's stalled timestamp %v, got %v", anchor.Add(-2*time.Hour), got.TS)
	})

	t.Run("ignores currencies no longer collected", func(t *testing.T) {
		// Dropping btc from the collected set must release the anchor —
		// otherwise a retired currency would freeze the window forever.
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk",
			[]string{"usd", "eur"}, horizon)
		require.NoError(t, err)
		require.True(t, got.Found)
		require.True(t, got.TS.Equal(anchor.Add(-time.Minute)),
			"want the fresh frontier %v, got %v", anchor.Add(-time.Minute), got.TS)
	})

	t.Run("a never-stored currency does not block the anchor", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk",
			[]string{"usd", "eur", "jpy"}, horizon)
		require.NoError(t, err)
		require.True(t, got.Found)
		require.True(t, got.TS.Equal(anchor.Add(-time.Minute)),
			"a currency with no rows has no history to heal; got %v", got.TS)
	})

	// The anti-pinning rule: a currency stalled beyond the caller's catch-up
	// horizon cannot be repaired by widening one window, so it must drop out of
	// the anchor instead of holding it at the horizon on every future tick.
	t.Run("a currency stalled past the horizon drops out", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk",
			[]string{"usd", "eur", "btc"}, anchor.Add(-time.Hour))
		require.NoError(t, err)
		require.True(t, got.Found)
		require.True(t, got.TS.Equal(anchor.Add(-time.Minute)),
			"btc is stalled past the horizon and must not pin the anchor; got %v", got.TS)
	})

	// The mirror image of the rule above: when EVERY metric is past the horizon
	// the feed as a whole was down, so the answer is the horizon itself and the
	// caller still covers as much of the outage as one window reaches. Returning
	// "no anchor" here would leave a total outage with no catch-up at all —
	// worse than having no horizon in the first place.
	t.Run("every metric past the horizon anchors at the horizon", func(t *testing.T) {
		horizonPastAll := anchor.Add(time.Hour)
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk",
			[]string{"usd", "eur", "btc"}, horizonPastAll)
		require.NoError(t, err)
		require.True(t, got.Found, "a total outage must still produce a catch-up anchor")
		require.True(t, got.TS.Equal(horizonPastAll),
			"want the horizon %v, got %v", horizonPastAll, got.TS)
	})

	t.Run("no rows at all yields no anchor", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "usdt",
			[]string{"usd", "eur"}, horizon)
		require.NoError(t, err)
		require.False(t, got.Found)
	})

	t.Run("isolated per token and source", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceEquiteez, "mvrk", []string{"usd"}, horizon)
		require.NoError(t, err)
		require.False(t, got.Found)

		got, err = repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "usdt", []string{"usd"}, horizon)
		require.NoError(t, err)
		require.False(t, got.Found)
	})

	t.Run("no metrics means no anchor", func(t *testing.T) {
		got, err := repo.LatestCommonTimestamp(ctx, prices.SourceCoinGecko, "mvrk", nil, horizon)
		require.NoError(t, err)
		require.False(t, got.Found)
	})
}

// The anchor query runs once per token on every live tick, so its cost must not
// scale with retained history: each metric's frontier is a seek on the
// (token, source, currency) index prefix. A GROUP BY formulation cannot use
// that index and reads every row instead.
//
// The plan is measured on the statement the REPOSITORY emits (captured through
// a gorm logger), not a hand-copy, so the assertion still guards the query if
// the SQL is ever rewritten. Buffers are the metric rather than plan-node names:
// a scan of this table also reports "Index Only Scan", so node names prove
// nothing — only pages read separate a seek from a scan.
func TestTokenPriceLatestCommonTimestamp_ReadsFewPages(t *testing.T) {
	rec := &legacySQLRecorder{}
	db := recordingGorm(t, rec)
	truncateTokenPrices(t, db)
	anchor := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	const samplesPerCurrency = 1000
	currencies := []string{"usd", "eur", "btc"}
	rows := make([]tokenPriceRow, 0, samplesPerCurrency*len(currencies))
	for i := 0; i < samplesPerCurrency; i++ {
		off := -time.Duration(i) * time.Minute
		for _, cur := range currencies {
			rows = append(rows, tokenPriceRow{off, cur, "1.00"})
		}
	}
	saveTokenPriceRows(t, db, "mvrk", prices.SourceCoinGecko, anchor, rows)
	require.NoError(t, db.Exec(`ANALYZE token_prices`).Error)

	got, err := repositories.NewTokenPriceRepository(db).LatestCommonTimestamp(
		context.Background(), prices.SourceCoinGecko, "mvrk", currencies, anchor.Add(-24*time.Hour))
	require.NoError(t, err)
	require.True(t, got.Found)

	var raw string
	require.NoError(t, db.Raw(`EXPLAIN (ANALYZE, FORMAT JSON) `+rec.last(t)).Scan(&raw).Error)
	var explained []struct {
		Plan map[string]any `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &explained))
	require.NotEmpty(t, explained)

	// Rows actually emitted by scan nodes, not pages: on a fixture small enough
	// to run fast, a scan and a seek read a similar number of PAGES, but a seek
	// emits one row per metric where a scan emits every sample.
	rowsScanned := scannedRows(explained[0].Plan)
	maxRows := 4 * len(currencies) // one frontier row per metric, plus the VALUES rows
	require.LessOrEqualf(t, rowsScanned, maxRows,
		"anchor query read %d rows out of a %d-row fixture — it is scanning history, not seeking:\n%s",
		rowsScanned, samplesPerCurrency*len(currencies), raw)
}

// scannedRows sums actual rows × loops over every scan node in an EXPLAIN JSON
// plan tree.
func scannedRows(node map[string]any) int {
	total := 0
	if nt, _ := node["Node Type"].(string); strings.Contains(nt, "Scan") {
		rows, _ := node["Actual Rows"].(float64)
		loops, _ := node["Actual Loops"].(float64)
		total += int(rows * loops)
	}
	if kids, ok := node["Plans"].([]any); ok {
		for _, k := range kids {
			if child, ok := k.(map[string]any); ok {
				total += scannedRows(child)
			}
		}
	}
	return total
}

// TestTokenPricesLatestIndexBuiltNonBlockingOnUpgrade exercises the real
// transition 0022/0023 perform on an EXISTING database, which the suite's
// fresh-DB run never reaches: there 0003 already created the replacement, so
// 0022 short-circuits on IF NOT EXISTS.
//
// The load-bearing part is that the build runs WITH
// (timescaledb.transaction_per_chunk) — it commits per chunk instead of locking
// the whole hypertable for the duration — which Postgres only permits outside a
// transaction block. That is why the two statements live in separate files, and
// this test would fail if they were merged back together.
func TestTokenPricesLatestIndexBuiltNonBlockingOnUpgrade(t *testing.T) {
	db := openGorm(t)
	dir, err := findMigrationsDir()
	require.NoError(t, err)

	indexExists := func(name string) bool {
		var n int64
		require.NoError(t, db.Raw(
			`SELECT count(*) FROM pg_class WHERE relkind = 'i' AND relname = ?`, name).Scan(&n).Error)
		return n > 0
	}

	// Recreate the pre-0022 shape: the superseded index present, the new one gone.
	require.NoError(t, db.Exec(`DROP INDEX IF EXISTS idx_token_prices_latest_source`).Error)
	require.NoError(t, db.Exec(
		`CREATE INDEX IF NOT EXISTS idx_token_prices_latest
		   ON token_prices (token_symbol, quote_currency, ts DESC)`).Error)
	t.Cleanup(func() {
		_ = db.Exec(`DROP INDEX IF EXISTS idx_token_prices_latest`).Error
		_ = db.Exec(
			`CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
			   ON token_prices (token_symbol, source_code, quote_currency, ts DESC)`).Error
	})

	// Replay EVERY migration in deploy order, not just 0022/0023: the runners
	// have no migration-tracking table and re-apply the whole directory on each
	// deploy, so an earlier file creating the same index with a plain
	// CREATE INDEX would win the race and silently make 0022's non-blocking
	// build a no-op. Only a full replay can catch that.
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	sort.Strings(files)
	for _, path := range files {
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NoErrorf(t, db.Exec(string(body)).Error, "applying %s", filepath.Base(path))
	}

	require.True(t, indexExists("idx_token_prices_latest_source"), "0022 must build the replacement index")
	require.False(t, indexExists("idx_token_prices_latest"), "0023 must drop the superseded index")

	// The build must be the non-blocking one, which only holds if 0022 is the
	// sole creator — grep the whole directory rather than trusting the outcome
	// above, which an earlier plain build would also satisfy.
	creators := make([]string, 0, 1)
	for _, path := range files {
		body, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		if strings.Contains(string(body), "CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source") {
			creators = append(creators, filepath.Base(path))
		}
	}
	require.Equal(t, []string{"0022_token_prices_latest_index.sql"}, creators,
		"exactly one migration may create this index, and it must be the transaction_per_chunk one")

	// Pin the reason for the split: merged into one file, the same two
	// statements become a single implicit transaction and the non-blocking
	// build is rejected outright.
	require.NoError(t, db.Exec(`DROP INDEX IF EXISTS idx_token_prices_latest_source`).Error)
	merged := `CREATE INDEX IF NOT EXISTS idx_token_prices_latest_source
	             ON token_prices (token_symbol, source_code, quote_currency, ts DESC)
	             WITH (timescaledb.transaction_per_chunk);
	           DROP INDEX IF EXISTS idx_token_prices_latest;`
	require.Error(t, db.Exec(merged).Error,
		"merging 0022 and 0023 must fail — that is why they are separate files")
}
