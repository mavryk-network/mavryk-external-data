package prices

import (
	"context"
	"errors"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

type negCacheFXSource struct {
	calls int
	price decimal.Decimal
	found bool
}

func (f *negCacheFXSource) LatestRateAtOrBefore(_ context.Context, _ prices.Source, _, _ string, at time.Time) (prices.PricePoint, bool, error) {
	f.calls++
	if !f.found {
		return prices.PricePoint{}, false, nil
	}
	return prices.PricePoint{Price: f.price, Timestamp: at}, true, nil
}

func newTestConv(f HistoricalFXSource) *tokenFXConverter {
	return &tokenFXConverter{
		repo:         f,
		maxStaleness: time.Minute,
		fxSource:     prices.SourceCoinGecko,
		cache:        make(map[fxCacheKey]fxCacheEntry),
	}
}

func TestLookupRate_NegativeCache_NoRepeatQuery(t *testing.T) {
	f := &negCacheFXSource{found: false}
	c := newTestConv(f)
	ts := time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC)

	for i := 0; i < 3; i++ {
		_, _, err := c.lookupRate(context.Background(), prices.Token("mvrk"), prices.Currency("usd"), ts)
		if !errors.Is(err, ErrNoFXRate) {
			t.Fatalf("call %d: want ErrNoFXRate, got %v", i, err)
		}
	}
	if f.calls != 1 {
		t.Errorf("repo hit %d times, want 1 (misses negative-cached for the minute bucket)", f.calls)
	}
}

func TestLookupRate_NonPositiveTreatedAsMissing(t *testing.T) {
	f := &negCacheFXSource{found: true, price: decimal.Zero}
	c := newTestConv(f)
	ts := time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC)

	_, _, err := c.lookupRate(context.Background(), prices.Token("mvrk"), prices.Currency("usd"), ts)
	if !errors.Is(err, ErrNoFXRate) {
		t.Fatalf("zero stored price must map to ErrNoFXRate, got %v", err)
	}
	// second call served from the negative cache, not re-queried
	_, _, _ = c.lookupRate(context.Background(), prices.Token("mvrk"), prices.Currency("usd"), ts)
	if f.calls != 1 {
		t.Errorf("repo hit %d times, want 1", f.calls)
	}
}

func TestLookupRate_PositiveCached(t *testing.T) {
	f := &negCacheFXSource{found: true, price: decimal.RequireFromString("2.5")}
	c := newTestConv(f)
	ts := time.Date(2026, 3, 1, 12, 0, 30, 0, time.UTC)

	rate, _, err := c.lookupRate(context.Background(), prices.Token("mvrk"), prices.Currency("usd"), ts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !rate.Equal(decimal.RequireFromString("2.5")) {
		t.Errorf("rate = %s, want 2.5", rate)
	}
	_, _, _ = c.lookupRate(context.Background(), prices.Token("mvrk"), prices.Currency("usd"), ts)
	if f.calls != 1 {
		t.Errorf("repo hit %d times, want 1 (positive hit cached)", f.calls)
	}
}
