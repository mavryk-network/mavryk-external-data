package tickers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"

	"github.com/shopspring/decimal"
)

// fakeRepo lets us count calls and replay arbitrary responses.
type fakeRepo struct {
	latestCalls       atomic.Int32
	distributionCalls atomic.Int32
	saveCalls         atomic.Int32
	latestResp        tickers.Snapshot
	distResp          tickers.Distribution
	latestErr         error
	distErr           error
	saveN             int64
}

func (f *fakeRepo) SaveSnapshot(_ context.Context, _ []tickers.Exchange, _ []tickers.Ticker) (int64, error) {
	f.saveCalls.Add(1)
	return f.saveN, nil
}

func (f *fakeRepo) LatestSnapshot(_ context.Context, _ LatestQuery) (tickers.Snapshot, error) {
	f.latestCalls.Add(1)
	if f.latestErr != nil {
		return tickers.Snapshot{}, f.latestErr
	}
	return f.latestResp, nil
}

func (f *fakeRepo) VolumeDistribution(_ context.Context, _ DistributionQuery) (tickers.Distribution, error) {
	f.distributionCalls.Add(1)
	if f.distErr != nil {
		return tickers.Distribution{}, f.distErr
	}
	return f.distResp, nil
}

func mvrk() prices.Token { return prices.Token("mvrk") }

func sampleSnapshot() tickers.Snapshot {
	return tickers.Snapshot{
		Token:     mvrk(),
		Source:    prices.SourceCoinGecko,
		Timestamp: time.Unix(1700000000, 0).UTC(),
		Rows: []tickers.SnapshotRow{
			{
				Exchange:     tickers.Exchange{ID: "binance", Name: "Binance"},
				TargetSymbol: "btc",
				LastPrice:    decimal.RequireFromString("0.000021"),
			},
		},
	}
}

func sampleDist() tickers.Distribution {
	return tickers.Distribution{
		Token:     mvrk(),
		Source:    prices.SourceCoinGecko,
		Timestamp: time.Unix(1700000000, 0).UTC(),
		GroupBy:   tickers.GroupByExchange,
		Total:     decimal.RequireFromString("1000"),
		Rows: []tickers.DistributionRow{
			{
				Exchange:   tickers.Exchange{ID: "binance"},
				VolumeBase: decimal.RequireFromString("1000"),
				SharePct:   decimal.RequireFromString("100.000000"),
			},
		},
	}
}

func TestCachedRepository_LatestServesCache(t *testing.T) {
	inner := &fakeRepo{latestResp: sampleSnapshot()}
	c := NewCachedRepository(inner, time.Minute, time.Minute)
	q := LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko, IncludeStale: false, StaleAfter: time.Hour}
	for i := 0; i < 5; i++ {
		if _, err := c.LatestSnapshot(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if got := inner.latestCalls.Load(); got != 1 {
		t.Fatalf("latestCalls = %d, want 1", got)
	}
}

func TestCachedRepository_LatestKeyIncludesIncludeStale(t *testing.T) {
	inner := &fakeRepo{latestResp: sampleSnapshot()}
	c := NewCachedRepository(inner, time.Minute, time.Minute)
	q1 := LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko, IncludeStale: false, StaleAfter: time.Hour}
	q2 := q1
	q2.IncludeStale = true
	for _, q := range []LatestQuery{q1, q2, q1, q2} {
		if _, err := c.LatestSnapshot(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if got := inner.latestCalls.Load(); got != 2 {
		t.Fatalf("latestCalls = %d, want 2 (different include_stale must miss)", got)
	}
}

func TestCachedRepository_DistributionKeyIncludesGroupBy(t *testing.T) {
	inner := &fakeRepo{distResp: sampleDist()}
	c := NewCachedRepository(inner, time.Minute, time.Minute)
	q1 := DistributionQuery{Token: mvrk(), Source: prices.SourceCoinGecko, GroupBy: tickers.GroupByExchange, StaleAfter: time.Hour}
	q2 := q1
	q2.GroupBy = tickers.GroupByTarget
	for _, q := range []DistributionQuery{q1, q2, q1, q2} {
		if _, err := c.VolumeDistribution(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if got := inner.distributionCalls.Load(); got != 2 {
		t.Fatalf("distributionCalls = %d, want 2 (different group_by must miss)", got)
	}
}

func TestCachedRepository_SaveInvalidatesBoth(t *testing.T) {
	inner := &fakeRepo{latestResp: sampleSnapshot(), distResp: sampleDist(), saveN: 5}
	c := NewCachedRepository(inner, time.Minute, time.Minute)
	ctx := context.Background()
	_, _ = c.LatestSnapshot(ctx, LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko})
	_, _ = c.VolumeDistribution(ctx, DistributionQuery{Token: mvrk(), Source: prices.SourceCoinGecko, GroupBy: tickers.GroupByExchange})
	if inner.latestCalls.Load() != 1 || inner.distributionCalls.Load() != 1 {
		t.Fatalf("setup: latestCalls=%d distCalls=%d", inner.latestCalls.Load(), inner.distributionCalls.Load())
	}
	// Save with rows for mvrk → both caches drop their mvrk entries.
	_, err := c.SaveSnapshot(ctx, nil, []tickers.Ticker{{Token: mvrk(), Source: prices.SourceCoinGecko}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.LatestSnapshot(ctx, LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko})
	_, _ = c.VolumeDistribution(ctx, DistributionQuery{Token: mvrk(), Source: prices.SourceCoinGecko, GroupBy: tickers.GroupByExchange})
	if inner.latestCalls.Load() != 2 {
		t.Errorf("latest miss after Save: latestCalls=%d want 2", inner.latestCalls.Load())
	}
	if inner.distributionCalls.Load() != 2 {
		t.Errorf("distribution miss after Save: distCalls=%d want 2", inner.distributionCalls.Load())
	}
}

func TestCachedRepository_LatestErrorNotCached(t *testing.T) {
	boom := errors.New("boom")
	inner := &fakeRepo{latestErr: boom}
	c := NewCachedRepository(inner, time.Minute, time.Minute)
	q := LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko}
	for i := 0; i < 3; i++ {
		if _, err := c.LatestSnapshot(context.Background(), q); !errors.Is(err, boom) {
			t.Fatalf("err=%v want boom", err)
		}
	}
	if got := inner.latestCalls.Load(); got != 3 {
		t.Fatalf("latestCalls = %d, want 3 (errors must not cache)", got)
	}
}

func TestCachedRepository_DisabledTTLPassesThrough(t *testing.T) {
	inner := &fakeRepo{latestResp: sampleSnapshot()}
	c := NewCachedRepository(inner, 0, 0)
	q := LatestQuery{Token: mvrk(), Source: prices.SourceCoinGecko}
	for i := 0; i < 3; i++ {
		if _, err := c.LatestSnapshot(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if got := inner.latestCalls.Load(); got != 3 {
		t.Fatalf("latestCalls = %d, want 3 (ttl=0 disables cache)", got)
	}
}
