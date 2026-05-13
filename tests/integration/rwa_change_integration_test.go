//go:build integration

package integration

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
)

// RWA price-change integration tests. The RWA change repo reads raw
// `rwa_quote_prices` (no CAs, per Decision #18). pair_id + side filters
// drive the PK index seek; currency is metadata only (echoed back into
// ChangeRepoResult so the service layer stays uniform with FT).

func TestRWAChange_HappyPath(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)

	saveRepo := repositories.NewRWAPriceRepository(db)
	changeRepo := repositories.NewRWAChangeRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	pidStr := strconv.FormatInt(pid, 10)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-30 * 24 * time.Hour), Metric: "last", Price: dec("50.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-7 * 24 * time.Hour), Metric: "last", Price: dec("54.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-24 * time.Hour), Metric: "last", Price: dec("55.80")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-30 * time.Second), Metric: "last", Price: dec("56.25")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  pidStr,
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        now,
	})
	require.NoError(t, err)

	require.Len(t, res.Now, 1)
	require.True(t, res.Now[0].Found)
	require.Equal(t, "usdt", res.Now[0].Currency, "currency is echoed as metadata")
	require.True(t, res.Now[0].Price.Equal(dec("56.25")))

	a24 := findAnchor(t, res.Anchors, "usdt", prices.Period24h)
	require.True(t, a24.Found)
	require.True(t, a24.Price.Equal(dec("55.80")))

	a7 := findAnchor(t, res.Anchors, "usdt", prices.Period7d)
	require.True(t, a7.Found)
	require.True(t, a7.Price.Equal(dec("54.00")))

	a30 := findAnchor(t, res.Anchors, "usdt", prices.Period30d)
	require.True(t, a30.Found)
	require.True(t, a30.Price.Equal(dec("50.00")))
}

// --- Sample after anchor window must NOT be picked ---

func TestRWAChange_AtOrBefore_ExcludesSamplesAfterWindow(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	saveRepo := repositories.NewRWAPriceRepository(db)
	changeRepo := repositories.NewRWAChangeRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	pidStr := strconv.FormatInt(pid, 10)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	hi := now.Add(-24 * time.Hour)
	pts := []prices.PricePoint{
		// In-window samples: latest is at hi-1m.
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: hi.Add(-30 * time.Minute), Metric: "last", Price: dec("50.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: hi.Add(-1 * time.Minute), Metric: "last", Price: dec("55.00")},
		// Out-of-window: must be ignored even though "close to now-24h" intuition might pick it.
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: hi.Add(5 * time.Minute), Metric: "last", Price: dec("99.99")},
		// Latest sample so res.Now is populated.
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-30 * time.Second), Metric: "last", Price: dec("60.00")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  pidStr,
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)
	a := findAnchor(t, res.Anchors, "usdt", prices.Period24h)
	require.True(t, a.Found)
	require.True(t, a.Price.Equal(dec("55.00")),
		"at-or-before must pick the latest in-window sample; got %s", a.Price)
}

// --- Pair / side isolation: bid/ask must not bleed into `last` change ---

func TestRWAChange_SideIsolation(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	saveRepo := repositories.NewRWAPriceRepository(db)
	changeRepo := repositories.NewRWAChangeRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	pidStr := strconv.FormatInt(pid, 10)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	// bid/ask rows at the same timestamps as `last`, but with very different
	// prices — change repo must filter by side='last' and ignore them.
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-30 * time.Second), Metric: "last", Price: dec("100.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-30 * time.Second), Metric: "bid", Price: dec("999.99")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-24 * time.Hour), Metric: "last", Price: dec("90.00")},
		{Source: prices.SourceEquiteez, EntityKey: pidStr, Timestamp: now.Add(-24 * time.Hour), Metric: "ask", Price: dec("1.23")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  pidStr,
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)
	require.True(t, res.Now[0].Price.Equal(dec("100.00")),
		"now must be `last` side, not bid/ask; got %s", res.Now[0].Price)
	a := findAnchor(t, res.Anchors, "usdt", prices.Period24h)
	require.True(t, a.Price.Equal(dec("90.00")),
		"24h anchor must be `last` side, got %s", a.Price)
}

// --- Pair isolation: different pair_id must not bleed ---

func TestRWAChange_PairIsolation(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	saveRepo := repositories.NewRWAPriceRepository(db)
	changeRepo := repositories.NewRWAChangeRepository(db)

	mars1 := insertRWAPair(t, db, "mars1", "usdt")
	mars2 := insertRWAPair(t, db, "mars2", "usdt")
	mars1Str := strconv.FormatInt(mars1, 10)
	mars2Str := strconv.FormatInt(mars2, 10)

	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	pts := []prices.PricePoint{
		{Source: prices.SourceEquiteez, EntityKey: mars1Str, Timestamp: now.Add(-30 * time.Second), Metric: "last", Price: dec("10.00")},
		{Source: prices.SourceEquiteez, EntityKey: mars2Str, Timestamp: now.Add(-30 * time.Second), Metric: "last", Price: dec("999.99")},
	}
	_, err := saveRepo.Save(context.Background(), pts)
	require.NoError(t, err)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  mars1Str,
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	require.NoError(t, err)
	require.True(t, res.Now[0].Price.Equal(dec("10.00")),
		"mars1 must not see mars2 prices; got %s", res.Now[0].Price)
}

// --- Empty pair: every field returns Found=false ---

func TestRWAChange_NoData_ReturnsAllMissing(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	changeRepo := repositories.NewRWAChangeRepository(db)
	pid := insertRWAPair(t, db, "mars1", "usdt")
	pidStr := strconv.FormatInt(pid, 10)

	res, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  pidStr,
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period1h, prices.Period24h, prices.Period7d, prices.Period30d},
		Now:        time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.Len(t, res.Now, 1)
	require.False(t, res.Now[0].Found)
	require.Len(t, res.Anchors, 4)
	for _, a := range res.Anchors {
		require.False(t, a.Found, "period %s should be Found=false on empty pair", a.Period)
	}
}

// --- Invalid pair_id → 400 INVALID_ARGUMENT ---

func TestRWAChange_BadPairID_InvalidArgument(t *testing.T) {
	db := openGorm(t)
	changeRepo := repositories.NewRWAChangeRepository(db)

	_, err := changeRepo.GetChange(context.Background(), apiprices.ChangeQuery{
		Source:     prices.SourceEquiteez,
		EntityKey:  "not-a-number",
		AuxKey:     "last",
		Currencies: []string{"usdt"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
	})
	require.Error(t, err, "non-numeric pair_id must be rejected")
}
