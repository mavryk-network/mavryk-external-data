//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestRWAMarketSnapshotsReplaceNullsAndRejectOlderObservations(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	ctx := context.Background()
	lookup := repositories.NewLookupRepository(db)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pair := prices.RWAPair{Source: prices.SourceEquiteez, TokenAddr: "asset", QuoteAddr: "quote", OrderbookAddr: "book", BaseSymbol: "test", QuoteSymbol: "usdt", InAllowlist: true}
	id, err := lookup.UpsertRWAPair(ctx, pair, now)
	require.NoError(t, err)
	repo := repositories.NewRWAMarketSnapshotRepository(db)
	bid, ask, last := decimal.RequireFromString("9007199254.740993"), decimal.RequireFromString("9007199254.740995"), decimal.NewFromInt(100)
	trade := now.Add(-time.Hour)
	expiry := now.Add(time.Minute)
	zero := 0
	row := prices.RWAMarketSnapshot{PairID: id, Network: "basenet", IndexerGeneration: "g", ObservedAt: now,
		TokenAddr: "asset", TokenID: &zero, QuoteAddr: "quote", QuoteTokenID: &zero, OrderbookAddr: "book", NativeQuote: "usdt",
		Bid: &bid, Ask: &ask, BidExpiresAt: &expiry, AskExpiresAt: &expiry, LastPrice: &last, LastTradeAt: &trade}
	require.NoError(t, repo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{row}))
	// Safe reruns preserve existing evidence and do not duplicate Timescale jobs.
	dir, err := findMigrationsDir()
	require.NoError(t, err)
	script, err := os.ReadFile(filepath.Join(dir, "0026_rwa_market_observations.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(script)).Error)

	got, found, err := repo.Snapshot(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, got.Bid.Equal(bid))
	require.True(t, got.Ask.Equal(ask))
	require.True(t, got.LastTradeAt.Equal(trade))
	require.True(t, got.AskExpiresAt.Equal(expiry))
	newer := row
	newer.ObservedAt = now.Add(time.Second)
	newer.Ask = nil
	newer.AskExpiresAt = nil
	require.NoError(t, repo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{newer}))
	// A delayed collector must not restore the old ask.
	require.NoError(t, repo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{row}))
	got, found, err = repo.Snapshot(ctx, id)
	require.NoError(t, err)
	require.True(t, found)
	require.Nil(t, got.Ask)
	require.Nil(t, got.AskExpiresAt)
	require.NotNil(t, got.BidExpiresAt)
	require.NotNil(t, got.Bid)
	require.True(t, newer.ObservedAt.Equal(got.ObservedAt))
	newer.ObservedAt = now.Add(2 * time.Second)
	newer.Bid = nil
	newer.BidExpiresAt = nil
	newer.LastPrice = nil
	newer.LastTradeAt = nil
	newer.TokenID = nil
	newer.QuoteTokenID = nil
	newer.Reason = "ambiguous_identity"
	require.NoError(t, repo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{newer}))
	got, _, err = repo.Snapshot(ctx, id)
	require.NoError(t, err)
	require.Nil(t, got.Bid)
	require.Nil(t, got.BidExpiresAt)
	require.Nil(t, got.Ask)
	require.Nil(t, got.LastPrice)
	require.Nil(t, got.LastTradeAt)
	require.Nil(t, got.TokenID)
	require.Nil(t, got.QuoteTokenID)
	require.Equal(t, "ambiguous_identity", got.Reason)
	history, err := repo.SnapshotsAt(ctx, id, []time.Time{now, now.Add(time.Second), now.Add(2 * time.Second), now.Add(time.Microsecond)})
	require.NoError(t, err)
	require.Len(t, history, 3)
	byTime := map[int64]prices.RWAMarketSnapshot{}
	for _, h := range history {
		byTime[h.ObservedAt.UnixMicro()] = h
	}
	first := byTime[now.UnixMicro()]
	require.True(t, first.Ask.Equal(ask), "later side disappearance must not rewrite original evidence")
	require.True(t, first.LastTradeAt.Equal(trade))
	require.True(t, first.AskExpiresAt.Equal(expiry))
	second := byTime[now.Add(time.Second).UnixMicro()]
	require.Nil(t, second.Ask)
	require.Nil(t, second.AskExpiresAt)
	third := byTime[now.Add(2*time.Second).UnixMicro()]
	require.Nil(t, third.LastPrice)
	require.Equal(t, "ambiguous_identity", third.Reason)
	changed := row
	different := decimal.NewFromInt(999)
	changed.LastPrice = &different
	require.NoError(t, repo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{changed}))
	history, err = repo.SnapshotsAt(ctx, id, []time.Time{now})
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.True(t, history[0].LastPrice.Equal(last), "duplicate timestamp must not mutate captured evidence")
	history, err = repo.SnapshotsAt(ctx, id, []time.Time{now.Add(time.Microsecond)})
	require.NoError(t, err)
	require.Empty(t, history, "no nearest-time fallback")
	_, found, err = repo.Snapshot(ctx, id+1)
	require.NoError(t, err)
	require.False(t, found)
}

func TestRWALaunchPricingIdentityRequiresResyncAndCanBeCleared(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	ctx := context.Background()
	repo := repositories.NewLaunchRepository(db)
	row := prices.RWALaunch{Source: prices.SourceEquiteez, TokenAddr: "asset", BaseSymbol: "asset", QuoteSymbol: "usdt", InAllowlist: true, Price: decimal.NewFromInt(100)}
	now := time.Now()
	require.NoError(t, repo.Upsert(ctx, row, now))
	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "asset", "usdt")
	require.NoError(t, err)
	require.True(t, found)
	require.Nil(t, got.PricingQuoteTokenID)
	zero := 0
	row.PricingQuoteTokenID = &zero
	require.NoError(t, repo.Upsert(ctx, row, now.Add(time.Second)))
	got, _, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "asset", "usdt")
	require.NoError(t, err)
	require.Equal(t, 0, *got.PricingQuoteTokenID)
	row.PricingQuoteTokenID = nil
	require.NoError(t, repo.Upsert(ctx, row, now.Add(2*time.Second)))
	got, _, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "asset", "usdt")
	require.NoError(t, err)
	require.Nil(t, got.PricingQuoteTokenID)
	require.Equal(t, "100", got.Price.String(), "legacy quote is unchanged")
}
