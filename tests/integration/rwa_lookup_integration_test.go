//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The natural key is (source_code, orderbook_addr), not the symbols — distinct
// addresses are what reach the ambiguity state.
func insertLookupPair(t *testing.T, db *gorm.DB, base, quote, orderbook string, enabled bool) int64 {
	t.Helper()
	var id int64
	err := db.Raw(`
		INSERT INTO rwa_pairs (base_symbol, quote_symbol, source_code, orderbook_addr, enabled)
		VALUES (?, ?, 'equiteez', ?, ?)
		RETURNING id
	`, base, quote, orderbook, enabled).Row().Scan(&id)
	require.NoError(t, err)
	require.NotZero(t, id, "rwa_pairs.id is zero")
	return id
}

func TestLookupRWAPairBySymbol_MatchesRegardlessOfStoredCase(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name        string
		storedBase  string
		storedQuote string
	}{
		{"stored upper", "MARS1", "USDT"},
		{"stored lower", "mars1", "usdt"},
		{"stored mixed", "MaRs1", "uSdT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openGorm(t)
			truncatePairs(t, db)
			repo := repositories.NewLookupRepository(db)

			id := insertLookupPair(t, db, tc.storedBase, tc.storedQuote, "KT1Book_"+tc.name, true)

			got, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")
			require.NoError(t, err)
			require.Equal(t, id, got.ID)
			require.Equal(t, tc.storedBase, got.BaseSymbol, "stored casing must be returned verbatim")
			require.Equal(t, tc.storedQuote, got.QuoteSymbol)
			require.Equal(t, prices.SourceEquiteez, got.Source)
			require.True(t, got.Enabled)
		})
	}
}

// The query lowercases the column, not the argument: a caller that skips
// parseRWASymbol silently 404s.
func TestLookupRWAPairBySymbol_ArgumentMustAlreadyBeLowercase(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	insertLookupPair(t, db, "MARS1", "USDT", "KT1BookArgCase", true)

	_, err := repo.LookupRWAPairBySymbol(ctx, "MARS1", "USDT")
	require.ErrorIs(t, err, prices.ErrPairNotFound)
}

func TestLookupRWAPairBySymbol_NotFound(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	_, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")
	require.ErrorIs(t, err, prices.ErrPairNotFound, "empty table must yield ErrPairNotFound")

	insertLookupPair(t, db, "mars1", "usdt", "KT1BookMars", true)

	_, err = repo.LookupRWAPairBySymbol(ctx, "mars1", "eurl")
	require.ErrorIs(t, err, prices.ErrPairNotFound, "quote must participate in the match")

	_, err = repo.LookupRWAPairBySymbol(ctx, "mars2", "usdt")
	require.ErrorIs(t, err, prices.ErrPairNotFound, "base must participate in the match")

	var ambiguous *prices.PairAmbiguousError
	require.False(t, errors.As(err, &ambiguous), "a miss is not an ambiguity")
}

func TestLookupRWAPairBySymbol_DisabledRowIsInvisible(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	disabledID := insertLookupPair(t, db, "MARS1", "USDT", "KT1BookDisabled", false)

	_, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")
	require.ErrorIs(t, err, prices.ErrPairNotFound, "a disabled-only match must read as not found")

	// The row itself is still addressable by id — only the symbol lookup filters.
	byID, err := repo.LookupRWAPair(ctx, disabledID)
	require.NoError(t, err)
	require.False(t, byID.Enabled)
}

// Disabling the spare row is the operator's fix for an ambiguity.
func TestLookupRWAPairBySymbol_DisabledDuplicateDoesNotTriggerAmbiguity(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	disabledID := insertLookupPair(t, db, "mars1", "usdt", "KT1BookRetired", false)
	enabledID := insertLookupPair(t, db, "MARS1", "USDT", "KT1BookLive", true)

	got, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")
	require.NoError(t, err)
	require.Equal(t, enabledID, got.ID)
	require.NotEqual(t, disabledID, got.ID)
}

func TestLookupRWAPairBySymbol_TwoEnabledRowsAreAmbiguous(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	// Addresses descend lexically while ids ascend, so a dropped `ORDER BY id`
	// shows up as a wrong IDs slice instead of passing.
	firstID := insertLookupPair(t, db, "mars1", "usdt", "KT1BookZ", true)
	secondID := insertLookupPair(t, db, "MARS1", "USDT", "KT1BookA", true)
	require.Less(t, firstID, secondID)

	_, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")
	require.Error(t, err)

	var ambiguous *prices.PairAmbiguousError
	require.True(t, errors.As(err, &ambiguous), "want *prices.PairAmbiguousError, got %T", err)
	require.Equal(t, "mars1", ambiguous.Base)
	require.Equal(t, "usdt", ambiguous.Quote)
	require.Equal(t, []int64{firstID, secondID}, ambiguous.IDs)
	require.NotErrorIs(t, err, prices.ErrPairNotFound)
}

// LIMIT 2 is deliberate: detecting the collision is enough.
func TestLookupRWAPairBySymbol_ThreeDuplicatesReportFirstTwoIDs(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	firstID := insertLookupPair(t, db, "mars1", "usdt", "KT1BookOne", true)
	secondID := insertLookupPair(t, db, "MARS1", "usdt", "KT1BookTwo", true)
	thirdID := insertLookupPair(t, db, "Mars1", "UsdT", "KT1BookThree", true)

	_, err := repo.LookupRWAPairBySymbol(ctx, "mars1", "usdt")

	var ambiguous *prices.PairAmbiguousError
	require.True(t, errors.As(err, &ambiguous), "want *prices.PairAmbiguousError, got %T", err)
	require.Equal(t, []int64{firstID, secondID}, ambiguous.IDs)
	require.NotContains(t, ambiguous.IDs, thirdID)
}

func TestLookupRWAPair_ByID(t *testing.T) {
	db := openGorm(t)
	truncatePairs(t, db)
	repo := repositories.NewLookupRepository(db)
	ctx := context.Background()

	id := insertLookupPair(t, db, "MARS1", "USDT", "KT1BookByID", true)

	got, err := repo.LookupRWAPair(ctx, id)
	require.NoError(t, err)
	require.Equal(t, id, got.ID)
	require.Equal(t, "KT1BookByID", got.OrderbookAddr)
	require.Empty(t, got.TokenAddr, "NULL token_addr must map to empty string")
	require.Nil(t, got.EquiteezOrderbookID)

	_, err = repo.LookupRWAPair(ctx, id+1000)
	require.ErrorIs(t, err, prices.ErrPairNotFound)
}
