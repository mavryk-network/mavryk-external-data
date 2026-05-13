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

// FT price-change integration tests. The change repo reads raw
// `token_prices` for both the `now` lookup and each period's at-or-before
// anchor (Decision #18 in the design doc — no CAs on this path). These
// tests prove the SQL composes correctly against a real TimescaleDB
// hypertable with the seeded lookup tables (0009_seed.sql).
//
// Mirrors token_charts_integration_test.go in style: truncate, seed via
// the production Save() path, query through the production repo, assert
// against the application-layer ChangeRepoResult.

// findAnchor is a tiny lookup helper — the assembler emits anchors in a
// stable (period, currency) order, but tests are easier to read when they
// look up by name.
func findAnchor(t *testing.T, anchors []apiprices.ChangeAnchor, currency string, period prices.Period) apiprices.ChangeAnchor {
	t.Helper()
	for _, a := range anchors {
		if a.Currency == currency && a.Period == period {
			return a
		}
	}
	t.Fatalf("no anchor for (%s, %s) in %+v", currency, period, anchors)
	return apiprices.ChangeAnchor{}
}

func findNow(t *testing.T, nows []apiprices.ChangeNow, currency string) apiprices.ChangeNow {
	t.Helper()
	for _, n := range nows {
		if n.Currency == currency {
			return n
		}
	}
	t.Fatalf("no now for %s in %+v", currency, nows)
	return apiprices.ChangeNow{}
}

// --- Happy path: now + 24h + 7d + 30d all found ---

func TestTokenChange_HappyPath(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)

	saveRepo := repositories.NewTokenPriceRepository(db)
	changeRepo := repositories.NewTokenChangeRepository(db)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	// Anchor samples for usd: now-30d, now-7d, now-24h, and the latest sample.
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-30 * 24 * time.Hour), Metric: "usd", Price: dec("0.0500")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-7 * 24 * time.Hour), Metric: "usd", Price: dec("0.0680")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-24 * time.Hour), Metric: "usd", Price: dec("0.0710")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-30 * time.Second), Metric: "usd", Price: dec("0.0715")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	})
	require.NoError(t, err)

	// Now: freshest usd sample at -30s.
	n := findNow(t, res.Now, "usd")
	require.True(t, n.Found)
	require.True(t, n.Price.Equal(dec("0.0715")), "now = %s", n.Price)

	// 24h anchor: the sample exactly at now-24h (inside the [now-25h, now-24h] window).
	a24 := findAnchor(t, res.Anchors, "usd", prices.Period24h)
	require.True(t, a24.Found, "24h anchor must be found")
	require.True(t, a24.Price.Equal(dec("0.0710")), "24h price = %s", a24.Price)

	// 7d / 30d anchors: same window logic, 12h staleness budget.
	a7 := findAnchor(t, res.Anchors, "usd", prices.Period7d)
	require.True(t, a7.Found)
	require.True(t, a7.Price.Equal(dec("0.0680")))

	a30 := findAnchor(t, res.Anchors, "usd", prices.Period30d)
	require.True(t, a30.Found)
	require.True(t, a30.Price.Equal(dec("0.0500")))
}

// --- Anchor outside staleness window -> Found=false (Decision #18) ---

func TestTokenChange_AnchorOutsideStaleness_NotFound(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	saveRepo := repositories.NewTokenPriceRepository(db)
	changeRepo := repositories.NewTokenChangeRepository(db)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	// 24h window is [now-25h, now-24h]. Place a sample 26 hours back — outside.
	// And a recent sample so `now` is found.
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-26 * time.Hour), Metric: "usd", Price: dec("0.06")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-1 * time.Minute), Metric: "usd", Price: dec("0.07")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)
	require.True(t, findNow(t, res.Now, "usd").Found)
	require.False(t, findAnchor(t, res.Anchors, "usd", prices.Period24h).Found,
		"sample at -26h is outside the 24h ±1h staleness window")
}

// --- Multi-currency: anchors picked independently per currency ---

func TestTokenChange_MultiCurrency_PerCurrencyAnchors(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	saveRepo := repositories.NewTokenPriceRepository(db)
	changeRepo := repositories.NewTokenChangeRepository(db)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		// usd has 24h anchor + now.
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-24 * time.Hour), Metric: "usd", Price: dec("1.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-1 * time.Minute), Metric: "usd", Price: dec("1.10")},
		// eur has only `now` — no 24h anchor.
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-30 * time.Second), Metric: "eur", Price: dec("0.85")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd", "eur"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)

	require.True(t, findNow(t, res.Now, "usd").Found)
	require.True(t, findNow(t, res.Now, "eur").Found)

	require.True(t, findAnchor(t, res.Anchors, "usd", prices.Period24h).Found,
		"usd 24h anchor should be found")
	require.False(t, findAnchor(t, res.Anchors, "eur", prices.Period24h).Found,
		"eur 24h anchor should be missing — only `now` was seeded")
}

// --- Empty token (never saved): every field returns Found=false ---

func TestTokenChange_NoData_ReturnsAllMissing(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	changeRepo := repositories.NewTokenChangeRepository(db)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.False(t, findNow(t, res.Now, "usd").Found)
	for _, p := range []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d} {
		require.False(t, findAnchor(t, res.Anchors, "usd", p).Found,
			"period %s should be Found=false on empty token", p)
	}
}

// --- At-or-before semantics: picks LATEST sample <= now-Δ, not the closest ---

func TestTokenChange_AtOrBefore_PicksLatestNotClosest(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	saveRepo := repositories.NewTokenPriceRepository(db)
	changeRepo := repositories.NewTokenChangeRepository(db)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	hi := now.Add(-24 * time.Hour)
	// Three samples inside [hi-1h, hi]; latest is at hi-1m.
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: hi.Add(-50 * time.Minute), Metric: "usd", Price: dec("1.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: hi.Add(-10 * time.Minute), Metric: "usd", Price: dec("2.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: hi.Add(-1 * time.Minute), Metric: "usd", Price: dec("3.00")}, // latest <= hi
		// Sample AFTER hi must be excluded (would violate at-or-before).
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: hi.Add(1 * time.Minute), Metric: "usd", Price: dec("99.99")},
		// `now` sample so res.Now is populated.
		{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now.Add(-30 * time.Second), Metric: "usd", Price: dec("10.00")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)
	a := findAnchor(t, res.Anchors, "usd", prices.Period24h)
	require.True(t, a.Found)
	require.True(t, a.Price.Equal(dec("3.00")),
		"anchor should be the LATEST sample at-or-before now-24h, got %s (expected 3.00)", a.Price)
	require.True(t, a.Bucket.Equal(hi.Add(-1*time.Minute)),
		"anchor bucket ts should match the sample ts, got %v", a.Bucket)
}

// --- LatestRateAtOrBefore (the FX-fix method that powers the converter) ---

func TestLatestRateAtOrBefore_PicksLatestBelowOrAtTimestamp(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: t1, Metric: "usd", Price: dec("1.0001")},
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: t2, Metric: "usd", Price: dec("1.0005")},
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: t3, Metric: "usd", Price: dec("0.9999")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)

	cases := []struct {
		name     string
		askAt    time.Time
		wantRate decimal.Decimal
		wantTS   time.Time
	}{
		{"at t1 exactly", t1, dec("1.0001"), t1},
		{"between t1 and t2", t1.Add(7 * 24 * time.Hour), dec("1.0001"), t1},
		{"at t2 exactly", t2, dec("1.0005"), t2},
		{"after t3", t3.Add(time.Hour), dec("0.9999"), t3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, found, err := repo.LatestRateAtOrBefore(context.Background(),
				prices.SourceCoinGecko, "usdt", "usd", c.askAt)
			require.NoError(t, err)
			require.True(t, found, "expected a row at-or-before %v", c.askAt)
			require.True(t, p.Price.Equal(c.wantRate),
				"rate = %s, want %s", p.Price, c.wantRate)
			require.True(t, p.Timestamp.Equal(c.wantTS),
				"ts = %v, want %v", p.Timestamp, c.wantTS)
		})
	}
}

func TestLatestRateAtOrBefore_BeforeBackfill_NotFound(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), Metric: "usd", Price: dec("1.0")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)

	_, found, err := repo.LatestRateAtOrBefore(context.Background(),
		prices.SourceCoinGecko, "usdt", "usd",
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.False(t, found, "no row exists at-or-before pre-backfill timestamp")
}

func TestLatestRateAtOrBefore_IsolatedBySourceAndCurrency(t *testing.T) {
	db := openGorm(t)
	truncateTokenPrices(t, db)
	repo := repositories.NewTokenPriceRepository(db)

	ts := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: ts, Metric: "usd", Price: dec("1.00")},
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Timestamp: ts, Metric: "eur", Price: dec("0.85")},
	}
	_, err := repo.Save(context.Background(), pts)
	require.NoError(t, err)

	// usd
	p, found, err := repo.LatestRateAtOrBefore(context.Background(),
		prices.SourceCoinGecko, "usdt", "usd", ts.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, p.Price.Equal(dec("1.00")))

	// eur — must not bleed across currency
	p, found, err = repo.LatestRateAtOrBefore(context.Background(),
		prices.SourceCoinGecko, "usdt", "eur", ts.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, p.Price.Equal(dec("0.85")))

	// unknown source — no rows
	_, found, err = repo.LatestRateAtOrBefore(context.Background(),
		prices.SourceEquiteez, "usdt", "usd", ts.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, found, "Equiteez source shouldn't see CoinGecko rows")
}
