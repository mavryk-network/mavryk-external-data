//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func truncatePairs(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("TRUNCATE TABLE rwa_pairs RESTART IDENTITY CASCADE").Error)
}

// TestLookupRepository_QuoteAddrRoundTrip exercises migration 0017 against a
// real database: the quote_addr column must survive the upsert round-trip, and
// a later sync that lost the currency rows (degraded indexer payload) must NOT
// wipe a previously-good address — same preservation contract as
// equiteez_orderbook_id.
func TestLookupRepository_QuoteAddrRoundTrip(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	pair := prices.RWAPair{
		Source:        prices.SourceEquiteez,
		BaseSymbol:    "ntbm",
		QuoteSymbol:   "usdt",
		TokenAddr:     "KT1NtbmToken",
		QuoteAddr:     "KT1UsdtQuote",
		OrderbookAddr: "KT1NtbmBook",
	}
	id, err := repo.UpsertRWAPair(ctx, pair, now)
	require.NoError(t, err)
	require.NotZero(t, id)

	got, err := repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "KT1UsdtQuote", got.QuoteAddr, "quote_addr must round-trip (migration 0017)")

	// Degraded sync: same orderbook, but the payload carried no currency rows.
	degraded := pair
	degraded.QuoteAddr = ""
	_, err = repo.UpsertRWAPair(ctx, degraded, now.Add(time.Minute))
	require.NoError(t, err)

	got, err = repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "KT1UsdtQuote", got.QuoteAddr,
		"an empty quote_addr in a later sync must not wipe the stored one")

	// A changed address (quote token migrated) does propagate.
	moved := pair
	moved.QuoteAddr = "KT1UsdtV2"
	_, err = repo.UpsertRWAPair(ctx, moved, now.Add(2*time.Minute))
	require.NoError(t, err)

	got, err = repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "KT1UsdtV2", got.QuoteAddr)

	// And the catalog lister carries it too — that is what /v1/pairs/rwa serves.
	list, err := repo.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "KT1UsdtV2", list[0].QuoteAddr)

	// Pre-0017 rows: quote_addr IS NULL must read back as "".
	require.NoError(t, db.Exec(
		"UPDATE rwa_pairs SET quote_addr = NULL WHERE id = ?", id).Error)
	got, err = repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Empty(t, got.QuoteAddr, "NULL quote_addr must map to empty string, not error")
}
