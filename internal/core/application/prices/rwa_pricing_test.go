package prices

import (
	"context"
	"errors"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type snapshotStub struct {
	row   prices.RWAMarketSnapshot
	found bool
	err   error
}

func (r snapshotStub) Snapshot(context.Context, int64) (prices.RWAMarketSnapshot, bool, error) {
	return r.row, r.found, r.err
}

type pricingFX struct {
	times   []time.Time
	stale   bool
	missing bool
}

func (f *pricingFX) Convert(_ context.Context, _ prices.Token, _ prices.Currency, amount decimal.Decimal, at time.Time) (ConversionResult, error) {
	f.times = append(f.times, at)
	if f.missing {
		return ConversionResult{}, errors.New("missing")
	}
	rate := decimal.RequireFromString("0.99")
	return ConversionResult{Rate: rate, Amount: amount.Mul(rate), RateTS: at.Add(-time.Second), Stale: f.stale}, nil
}

func pricingFixture() (*RWAPricingService, prices.RWAPair, prices.RWAMarketSnapshot, *pricingFX) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	trade := now.Add(-24 * time.Hour)
	zero := 0
	bid, ask, last := decimal.NewFromInt(108), decimal.NewFromInt(112), decimal.NewFromInt(100)
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, TokenAddr: "KT1asset", QuoteAddr: "KT1quote", OrderbookAddr: "KT1book", BaseSymbol: "rwa", QuoteSymbol: "usdt", IndexerGeneration: "generation"}
	snap := prices.RWAMarketSnapshot{PairID: 1, Network: "basenet", IndexerGeneration: "generation", ObservedAt: now.Add(-time.Second),
		TokenAddr: pair.TokenAddr, TokenID: &zero, QuoteAddr: pair.QuoteAddr, QuoteTokenID: &zero,
		OrderbookAddr: pair.OrderbookAddr, NativeQuote: "usdt", Bid: &bid, Ask: &ask, LastPrice: &last, LastTradeAt: &trade}
	fx := &pricingFX{}
	prices.RegisterTokens([]prices.TokenInfo{{Symbol: "usdt", Decimals: 6, Enabled: true}})
	svc := &RWAPricingService{Network: "basenet", MaxAge: 3 * time.Minute, Now: func() time.Time { return now }, Converter: fx, Snapshots: snapshotStub{row: snap, found: true}}
	return svc, pair, snap, fx
}

func TestRWAPricingSeparatesMidpointAndExecutionFX(t *testing.T) {
	svc, pair, snap, fx := pricingFixture()
	out, err := svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, "110", *out.MarketPrice.Price)
	require.Equal(t, "108.9", *out.MarketPrice.USD)
	require.Equal(t, "100", *out.LastSale.Price)
	require.Equal(t, "99", *out.LastSale.USD)
	require.Equal(t, snap.LastTradeAt, out.LastSale.ExecutedAt)
	require.Equal(t, []time.Time{snap.ObservedAt, *snap.LastTradeAt}, fx.times)
	require.Equal(t, "0", *out.TokenID)
	require.Equal(t, "0", *out.QuoteTokenID)
	require.Equal(t, snap.ObservedAt.Add(svc.MaxAge), *out.ValidUntil)
}

func TestRWAPricingNoTradeStillHasMarket(t *testing.T) {
	svc, pair, snap, _ := pricingFixture()
	snap.LastPrice, snap.LastTradeAt = nil, nil
	svc.Snapshots = snapshotStub{row: snap, found: true}
	out, err := svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, "available", out.MarketPrice.Status)
	require.Nil(t, out.LastSale.Price)
	require.Nil(t, out.LastSale.ExecutedAt)
	require.Equal(t, "no_trade", *out.LastSale.Reason)
}

func TestRWAPricingRejectsInvalidMarketWithoutLastFallback(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		change       func(*prices.RWAMarketSnapshot)
	}{
		{"one sided", "one_sided_orderbook", func(s *prices.RWAMarketSnapshot) { s.Ask = nil }},
		{"empty", "empty_orderbook", func(s *prices.RWAMarketSnapshot) { s.Ask, s.Bid = nil, nil }},
		{"crossed", "crossed_orderbook", func(s *prices.RWAMarketSnapshot) { s.Ask, s.Bid = s.Bid, s.Ask }},
		{"stale", "snapshot_stale", func(s *prices.RWAMarketSnapshot) { s.ObservedAt = s.ObservedAt.Add(-3 * time.Minute) }},
		{"future", "invalid_observation_time", func(s *prices.RWAMarketSnapshot) { s.ObservedAt = s.ObservedAt.Add(time.Hour) }},
		{"generation", "generation_mismatch", func(s *prices.RWAMarketSnapshot) { s.IndexerGeneration = "old" }},
		{"contract", "identity_mismatch", func(s *prices.RWAMarketSnapshot) { s.TokenAddr = "wrong" }},
		{"network", "identity_mismatch", func(s *prices.RWAMarketSnapshot) { s.Network = "ghostnet" }},
		{"quote", "identity_mismatch", func(s *prices.RWAMarketSnapshot) { s.QuoteAddr = "wrong" }},
		{"book", "identity_mismatch", func(s *prices.RWAMarketSnapshot) { s.OrderbookAddr = "wrong" }},
		{"token id", "unsupported_identity", func(s *prices.RWAMarketSnapshot) { v := 1; s.TokenID = &v }},
		{"unknown id", "unsupported_identity", func(s *prices.RWAMarketSnapshot) { s.TokenID = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, pair, snap, _ := pricingFixture()
			test.change(&snap)
			svc.Snapshots = snapshotStub{row: snap, found: true}
			out, err := svc.Get(context.Background(), &pair, nil)
			require.NoError(t, err)
			require.Equal(t, test.reason, *out.MarketPrice.Reason)
			require.Nil(t, out.MarketPrice.Price)
			require.Nil(t, out.MarketPrice.USD)
			if test.name == "stale" {
				require.Equal(t, "100", *out.LastSale.Price)
				require.Equal(t, "stale", out.LastSale.Status)
				require.Equal(t, "snapshot_stale", *out.LastSale.Reason)
			}
		})
	}
}

func TestRWAPricingMissingOrStaleFXKeepsNative(t *testing.T) {
	for _, state := range []string{"missing", "stale"} {
		t.Run(state, func(t *testing.T) {
			svc, pair, _, fx := pricingFixture()
			fx.missing, fx.stale = state == "missing", state == "stale"
			out, err := svc.Get(context.Background(), &pair, nil)
			require.NoError(t, err)
			for _, q := range []PricingQuote{out.MarketPrice, out.LastSale.PricingQuote} {
				require.Equal(t, "available", q.Status)
				require.NotNil(t, q.Price)
				require.Nil(t, q.USD)
				require.Equal(t, state, q.FXStatus)
			}
		})
	}
}

func TestRWAPricingDoesNotInventExecutionTime(t *testing.T) {
	svc, pair, snap, _ := pricingFixture()
	snap.LastTradeAt = nil
	svc.Snapshots = snapshotStub{row: snap, found: true}
	out, err := svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, "execution_time_missing", *out.LastSale.Reason)
	require.Nil(t, out.LastSale.Price)
	require.Nil(t, out.LastSale.ExecutedAt)
	future := snap.ObservedAt.Add(time.Minute)
	snap.LastTradeAt = &future
	svc.Snapshots = snapshotStub{row: snap, found: true}
	out, err = svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, "invalid_execution_time", *out.LastSale.Reason)
}

func TestRWAPricingPrimaryIsSeparateAndExpiresOnRead(t *testing.T) {
	svc, _, snap, fx := pricingFixture()
	zero := 0
	launch := prices.RWALaunch{TokenAddr: "KT1asset", QuoteAddr: "KT1quote", TokenID: 0, PricingQuoteTokenID: &zero,
		BaseSymbol: "rwa", QuoteSymbol: "usdt", Price: decimal.NewFromInt(90), Active: true, Status: "active", LastSyncedAt: snap.ObservedAt}
	out, err := svc.Get(context.Background(), nil, &launch)
	require.NoError(t, err)
	require.Equal(t, "primary_market", *out.MarketPrice.Reason)
	require.Nil(t, out.LastSale.Price)
	require.Equal(t, "primary_list_price", out.PrimaryIssuance.Method)
	require.Equal(t, "90", *out.PrimaryIssuance.Price)
	require.Equal(t, "89.1", *out.PrimaryIssuance.USD)
	require.NotNil(t, out.ValidUntil)
	fx.stale = true
	out, _ = svc.Get(context.Background(), nil, &launch)
	require.Nil(t, out.PrimaryIssuance.USD)
	require.Equal(t, "stale", out.PrimaryIssuance.FXStatus)
	svc.Now = func() time.Time { return snap.ObservedAt.Add(3 * time.Minute) }
	out, _ = svc.Get(context.Background(), nil, &launch)
	require.Equal(t, "snapshot_stale", *out.PrimaryIssuance.Reason)
	require.Nil(t, out.PrimaryIssuance.Price)
	launch.LastSyncedAt = svc.Now()
	end := svc.Now().Add(-time.Second)
	launch.SaleEnd = &end
	out, _ = svc.Get(context.Background(), nil, &launch)
	require.Nil(t, out.PrimaryIssuance.USD)
	require.Equal(t, end, *out.ValidUntil)
}

func TestRWAPricingMidpointRetainsDecimalPrecision(t *testing.T) {
	svc, pair, snap, _ := pricingFixture()
	bid, ask := decimal.RequireFromString("1.000000000000000001"), decimal.RequireFromString("1.000000000000000002")
	snap.Bid, snap.Ask = &bid, &ask
	svc.Snapshots = snapshotStub{row: snap, found: true}
	out, err := svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, "1.0000000000000000015", *out.MarketPrice.Price)
}

func TestRWAPricingNewOrderbookKeepsVerifiedPrimaryIdentity(t *testing.T) {
	svc, pair, snap, _ := pricingFixture()
	svc.Snapshots = snapshotStub{}
	zero := 0
	launch := prices.RWALaunch{TokenAddr: pair.TokenAddr, QuoteAddr: pair.QuoteAddr, TokenID: 0, PricingQuoteTokenID: &zero,
		BaseSymbol: "rwa", QuoteSymbol: "usdt", Price: decimal.NewFromInt(90), Active: true, Status: "active", LastSyncedAt: snap.ObservedAt}
	out, err := svc.Get(context.Background(), &pair, &launch)
	require.NoError(t, err)
	require.Equal(t, "snapshot_missing", *out.MarketPrice.Reason)
	require.Equal(t, "0", *out.TokenID)
	require.Equal(t, "0", *out.QuoteTokenID)
	require.NotNil(t, out.ValidUntil)
	require.Equal(t, "90", *out.PrimaryIssuance.Price)
}

func TestRWAPricingMarketExpiresWithSelectedOrderWithoutExpiringLastSale(t *testing.T) {
	svc, pair, snap, _ := pricingFixture()
	bidEnd, askEnd := svc.Now().Add(time.Minute), svc.Now().Add(2*time.Minute)
	snap.BidExpiresAt, snap.AskExpiresAt = &bidEnd, &askEnd
	svc.Snapshots = snapshotStub{row: snap, found: true}
	out, err := svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Equal(t, bidEnd, *out.MarketPrice.ValidUntil)
	require.Equal(t, snap.ObservedAt.Add(svc.MaxAge), *out.LastSale.ValidUntil)
	svc.Now = func() time.Time { return bidEnd }
	out, err = svc.Get(context.Background(), &pair, nil)
	require.NoError(t, err)
	require.Nil(t, out.MarketPrice.USD)
	require.Nil(t, out.MarketPrice.Price)
	require.Equal(t, "order_expired", *out.MarketPrice.Reason)
	require.Equal(t, bidEnd, *out.MarketPrice.ValidUntil)
	require.Equal(t, "available", out.LastSale.Status)
	require.Equal(t, "100", *out.LastSale.Price)
}
