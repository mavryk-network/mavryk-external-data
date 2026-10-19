//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func truncatePairs(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec("TRUNCATE TABLE rwa_pairs RESTART IDENTITY CASCADE").Error)
}

// Migration 0017: a degraded indexer payload must not wipe a good quote_addr.
func TestLookupRepository_QuoteAddrRoundTrip(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	pair := prices.RWAPair{InAllowlist: true, //nolint:gosec // G101: synthetic public contract addresses, not credentials.
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

	moved := pair
	moved.QuoteAddr = "KT1UsdtV2"
	_, err = repo.UpsertRWAPair(ctx, moved, now.Add(2*time.Minute))
	require.NoError(t, err)

	got, err = repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "KT1UsdtV2", got.QuoteAddr)

	// The catalog lister is what /v1/pairs/rwa serves.
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

func TestCRMAllowlistPreservesLegacyUnknownRelationships(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	lookup := repositories.NewLookupRepository(db)
	launches := repositories.NewLaunchRepository(db)
	ctx := context.Background()
	_, err := lookup.UpsertRWAPair(ctx, prices.RWAPair{
		Source: prices.SourceEquiteez, TokenAddr: recoveryBase, OrderbookAddr: recoveryBook,
		BaseSymbol: "legacy", QuoteSymbol: "usdt", InAllowlist: true,
	}, time.Now())
	require.NoError(t, err)
	require.NoError(t, launches.Upsert(ctx, prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: recoveryLaunchToken, BaseSymbol: "primary", QuoteSymbol: "usdt", InAllowlist: true,
	}, time.Now()))
	a := equiteez.ContractAllowlist{SchemaVersion: 1, BaseTokens: []string{recoveryBase, recoveryLaunchToken},
		QuoteTokens: []string{recoveryQuote}, Orderbooks: []string{recoveryBook}, Launchpads: []string{recoveryLaunchpad}}
	require.NoError(t, lookup.ApplyAllowlist(ctx, "https://example.test/allowlist", a, false))
	pairs, err := lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Len(t, pairs, 1, "legacy NULL quote_addr must not hide saved history during reindexing")
	rows, err := launches.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	// An explicitly empty relationship list is authoritative even when its
	// exact address has not yet been discovered for legacy rows.
	a.QuoteTokens = []string{}
	require.NoError(t, lookup.ApplyAllowlist(ctx, "https://example.test/allowlist", a, false))
	pairs, err = lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Empty(t, pairs)
	rows, err = launches.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Empty(t, rows)
	// A later unknown relationship must not resurrect a previously delisted row.
	a.QuoteTokens = []string{recoveryQuote}
	require.NoError(t, lookup.ApplyAllowlist(ctx, "https://example.test/allowlist", a, false))
	pairs, err = lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Empty(t, pairs)
	require.NoError(t, db.Exec("UPDATE rwa_launches SET in_allowlist=TRUE").Error)
	a.Launchpads = []string{}
	require.NoError(t, lookup.ApplyAllowlist(ctx, "https://example.test/allowlist", a, false))
	rows, err = launches.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Empty(t, rows)
}
