//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func truncateLaunches(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("TRUNCATE TABLE rwa_launches").Error)
}

// TestLaunchRepository_RoundTrip exercises migration 0016 and the launch SQL
// against a real TimescaleDB: the NUMERIC(78,0) raw-nat columns, the ON CONFLICT
// clause, and the case-insensitive symbol lookup. Unit tests cannot cover any of
// this — they never touch a driver.
func TestLaunchRepository_RoundTrip(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)

	saleStart := now.Add(-24 * time.Hour)
	launch := prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1KHBE", TokenID: 0,
		LaunchID: 6, Name: "KHBE-issuance-v2", Status: "active", Active: true,
		BaseSymbol: "khbe", QuoteSymbol: "usdt",
		Price:           decimal.RequireFromString("100"),
		TotalBought:     decimal.RequireFromString("6667"),
		MaxAmountCap:    decimal.RequireFromString("2500000000000"),
		ProgressPercent: 2.667e-7,
		SaleStart:       &saleStart,
	}
	require.NoError(t, repo.Upsert(ctx, launch, now))

	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "khbe", "usdt")
	require.NoError(t, err)
	require.True(t, found)

	// Raw nats must survive the driver round-trip exactly — the whole reason the
	// columns are NUMERIC(78,0) rather than BIGINT.
	require.Equal(t, "6667", got.TotalBought.String())
	require.Equal(t, "2500000000000", got.MaxAmountCap.String())
	require.True(t, got.Price.Equal(decimal.RequireFromString("100")), "price = %s", got.Price)
	require.InDelta(t, 2.667e-7, got.ProgressPercent, 1e-12, "tiny progress must not collapse to 0")
	require.Equal(t, "active", got.Status)
	require.True(t, got.Active)
	require.False(t, got.LastSyncedAt.IsZero(), "LastSyncedAt backs the price_as_of the API serves")
	require.NotNil(t, got.SaleStart)
	require.True(t, saleStart.Equal(got.SaleStart.UTC()))

	// Case-insensitive lookup: the URL parser lowercases, the DB may not.
	_, found, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "KHBE", "USDT")
	require.NoError(t, err)
	require.True(t, found, "symbol lookup must be case-insensitive")

	// A symbol that is not a primary-market asset is a clean miss, not an error.
	_, found, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "nope", "usdt")
	require.NoError(t, err)
	require.False(t, found)
}

// A supply-scale cap of an 18-decimal token overflows int64. NUMERIC(78,0) must
// carry it losslessly, and ProgressPercent must still resolve rather than
// collapsing to 0 or NaN.
func TestLaunchRepository_SupplyScaleAmounts(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()

	const hugeCap = "1000000000000000000000000" // 1e24 — far beyond int64
	const bought = "500000000000000000000000"   // exactly half
	require.NoError(t, repo.Upsert(ctx, prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1BIG", LaunchID: 1,
		BaseSymbol: "big", QuoteSymbol: "usdt", Status: "active",
		Price:           decimal.RequireFromString("1.234567"),
		TotalBought:     decimal.RequireFromString(bought),
		MaxAmountCap:    decimal.RequireFromString(hugeCap),
		ProgressPercent: prices.ProgressPercent(bought, hugeCap),
	}, time.Now().UTC()))

	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "big", "usdt")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hugeCap, got.MaxAmountCap.String(), "1e24 cap must not be truncated")
	require.Equal(t, bought, got.TotalBought.String())
	require.InDelta(t, 50.0, got.ProgressPercent, 1e-9)
}

// `enabled` is the operator kill-switch: a routine sync must never resurrect an
// asset someone deliberately hid, so it is written on INSERT only.
func TestLaunchRepository_UpsertPreservesOperatorDisable(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()

	base := prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1OPS", LaunchID: 2,
		BaseSymbol: "ops", QuoteSymbol: "usdt", Status: "active",
		Price:        decimal.RequireFromString("10"),
		TotalBought:  decimal.Zero,
		MaxAmountCap: decimal.RequireFromString("100"),
	}
	require.NoError(t, repo.Upsert(ctx, base, time.Now().UTC()))

	// Operator hides the asset.
	require.NoError(t, db.Exec(
		"UPDATE rwa_launches SET enabled = FALSE WHERE token_addr = ?", "KT1OPS").Error)

	// A later sync updates the mutable state...
	base.Price = decimal.RequireFromString("11")
	base.TotalBought = decimal.RequireFromString("42")
	require.NoError(t, repo.Upsert(ctx, base, time.Now().UTC()))

	var enabled bool
	require.NoError(t, db.Raw(
		"SELECT enabled FROM rwa_launches WHERE token_addr = ?", "KT1OPS").Scan(&enabled).Error)
	require.False(t, enabled, "sync must not re-enable an operator-disabled launch")

	// ...and the disabled row stays out of the catalog and the symbol lookup.
	list, err := repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Empty(t, list)
	_, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "ops", "usdt")
	require.NoError(t, err)
	require.False(t, found)

	// But the refreshed values did land.
	var price decimal.Decimal
	require.NoError(t, db.Raw(
		"SELECT price FROM rwa_launches WHERE token_addr = ?", "KT1OPS").Scan(&price).Error)
	require.True(t, price.Equal(decimal.RequireFromString("11")), "price = %s", price)
}

// TestLaunchRepository_QuoteAddrRoundTrip exercises migration 0018: the
// payment-token address must survive the driver, and — same preserve contract
// as rwa_pairs.quote_addr — a later sync whose payment row lost its token ref
// must NOT wipe a previously-good address.
func TestLaunchRepository_QuoteAddrRoundTrip(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	l := prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1QA", LaunchID: 1,
		BaseSymbol: "qa", QuoteSymbol: "usdt", Status: "active",
		QuoteAddr:    "KT19bKTsUsdtPay",
		Price:        decimal.RequireFromString("100"),
		TotalBought:  decimal.Zero,
		MaxAmountCap: decimal.RequireFromString("1000"),
	}
	require.NoError(t, repo.Upsert(ctx, l, now))

	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "qa", "usdt")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "KT19bKTsUsdtPay", got.QuoteAddr, "quote_addr must round-trip (migration 0018)")

	// Degraded sync: payment row without a token ref → empty QuoteAddr.
	degraded := l
	degraded.QuoteAddr = ""
	require.NoError(t, repo.Upsert(ctx, degraded, now.Add(time.Minute)))

	got, _, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "qa", "usdt")
	require.NoError(t, err)
	require.Equal(t, "KT19bKTsUsdtPay", got.QuoteAddr,
		"an empty quote_addr in a later sync must not wipe the stored one")

	// A changed address (payment token migrated) does propagate.
	moved := l
	moved.QuoteAddr = "KT1UsdtV2"
	require.NoError(t, repo.Upsert(ctx, moved, now.Add(2*time.Minute)))

	list, err := repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "KT1UsdtV2", list[0].QuoteAddr, "the catalog lister must carry the refreshed address")
}

// EnabledLaunches must return a deterministic, symbol-ordered catalog — the API
// relies on it for a stable response.
func TestLaunchRepository_EnabledLaunchesOrdered(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, sym := range []string{"zeta", "alpha", "mid"} {
		require.NoError(t, repo.Upsert(ctx, prices.RWALaunch{
			Source: prices.SourceEquiteez, TokenAddr: "KT1" + sym, LaunchID: 1,
			BaseSymbol: sym, QuoteSymbol: "usdt", Status: "active",
			Price: decimal.RequireFromString("1"), TotalBought: decimal.Zero,
			MaxAmountCap: decimal.RequireFromString("1"),
		}, now))
	}
	list, err := repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 3)
	require.Equal(t, []string{"alpha", "mid", "zeta"},
		[]string{list[0].BaseSymbol, list[1].BaseSymbol, list[2].BaseSymbol})
}

// TestBackfillState_CursorTsRoundTrip exercises migration 0015: the fill-time
// keyset cursor must survive the driver, and ClearCaughtUp must resume only the
// legacy caught_up rows while leaving terminal/operator disables alone.
func TestBackfillState_CursorTsRoundTrip(t *testing.T) {
	db := openGorm(t)
	require.NoError(t, db.Exec("TRUNCATE TABLE backfill_state").Error)
	repo := repositories.NewBackfillStateRepository(db)
	ctx := context.Background()

	cursorTs := time.Date(2026, 7, 23, 14, 47, 47, 0, time.UTC)
	cursorID := int64(225)
	require.NoError(t, repo.Upsert(ctx, &repositories.BackfillState{
		Source: prices.SourceEquiteez, EntityKey: "7",
		CursorTs: &cursorTs, CursorID: &cursorID,
	}))

	got, err := repo.Get(ctx, prices.SourceEquiteez, "7")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.CursorTs, "cursor_ts column must round-trip (migration 0015)")
	require.True(t, cursorTs.Equal(got.CursorTs.UTC()), "got %v", got.CursorTs)
	require.NotNil(t, got.CursorID)
	require.Equal(t, int64(225), *got.CursorID)

	// Legacy sticky rows: one caught_up (resumable) and one manual (terminal).
	require.NoError(t, db.Exec(`UPDATE backfill_state
		SET disabled = TRUE, disabled_reason = 'caught_up' WHERE entity_key = '7'`).Error)
	require.NoError(t, repo.Upsert(ctx, &repositories.BackfillState{
		Source: prices.SourceEquiteez, EntityKey: "9",
		Disabled: true, DisabledReason: repositories.BackfillDisabledReasonManual,
	}))

	resumed, err := repo.ClearCaughtUp(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Equal(t, int64(1), resumed, "only the caught_up row should resume")

	back, err := repo.Get(ctx, prices.SourceEquiteez, "7")
	require.NoError(t, err)
	require.False(t, back.Disabled, "caught_up pair must be re-enabled on deploy")
	require.NotNil(t, back.CursorTs, "resuming must not rewind the cursor")
	require.Equal(t, int64(225), *back.CursorID)

	manual, err := repo.Get(ctx, prices.SourceEquiteez, "9")
	require.NoError(t, err)
	require.True(t, manual.Disabled, "an operator disable must survive ClearCaughtUp")
	require.Equal(t, repositories.BackfillDisabledReasonManual, manual.DisabledReason)
}

// TestBackfillState_ClearAutoDisabled: rows bricked by the legacy error
// threshold resume on deploy; manual disables survive.
func TestBackfillState_ClearAutoDisabled(t *testing.T) {
	db := openGorm(t)
	require.NoError(t, db.Exec("TRUNCATE TABLE backfill_state").Error)
	repo := repositories.NewBackfillStateRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, &repositories.BackfillState{
		Source: prices.SourceCoinGecko, EntityKey: "mvrk",
		Disabled: true, DisabledReason: repositories.BackfillDisabledReasonAutoDisabled,
		ErrorCount: 5, LastError: "coingecko returned status 502",
	}))
	require.NoError(t, repo.Upsert(ctx, &repositories.BackfillState{
		Source: prices.SourceCoinGecko, EntityKey: "usdt",
		Disabled: true, DisabledReason: repositories.BackfillDisabledReasonManual,
	}))

	resumed, err := repo.ClearAutoDisabled(ctx, prices.SourceCoinGecko)
	require.NoError(t, err)
	require.Equal(t, int64(1), resumed, "only the auto_disabled row should resume")

	back, err := repo.Get(ctx, prices.SourceCoinGecko, "mvrk")
	require.NoError(t, err)
	require.False(t, back.Disabled)
	require.Zero(t, back.ErrorCount)
	require.Empty(t, back.LastError)

	manual, err := repo.Get(ctx, prices.SourceCoinGecko, "usdt")
	require.NoError(t, err)
	require.True(t, manual.Disabled, "an operator disable must survive ClearAutoDisabled")
}
