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
		t.Errorf("repo hit %d times, want 1 (misses negative-cached for the requested instant)", f.calls)
	}
}

func TestFXCache_BoundsLiveHistoricalEntries(t *testing.T) {
	c := newTestConv(&negCacheFXSource{})
	entry := fxCacheEntry{expires: time.Now().Add(fxCacheTTL), found: true, rate: decimal.NewFromInt(1)}
	for i := 0; i < maxFXCacheEntries; i++ {
		c.cache[fxCacheKey{AtUnixNano: int64(i)}] = entry
	}
	c.store(fxCacheKey{AtUnixNano: maxFXCacheEntries}, entry)
	if len(c.cache) > maxFXCacheEntries {
		t.Fatalf("live historical cache exceeded its bound: %d", len(c.cache))
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

func TestConverter_AtOrBeforeWithinMinute(t *testing.T) {
	registerFXTokens(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, reverse := range []bool{false, true} {
		name := "earlier_then_later"
		if reverse {
			name = "later_then_earlier"
		}
		t.Run(name, func(t *testing.T) {
			source := &fakeFXSource{points: []prices.PricePoint{
				{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: base, Price: decimal.RequireFromString("1")},
				{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: base.Add(30 * time.Second), Price: decimal.RequireFromString("0.9")},
			}}
			conv := NewTokenFXConverter(source, time.Minute, prices.SourceCoinGecko)
			seconds := []int{10, 50}
			expected := []string{"1", "0.9"}
			if reverse {
				seconds = []int{50, 10}
				expected = []string{"0.9", "1"}
			}
			for i, sec := range seconds {
				at := base.Add(time.Duration(sec) * time.Second)
				got, err := conv.Convert(context.Background(), prices.Token("usdt"), prices.Currency("usd"), decimal.NewFromInt(100), at)
				if err != nil {
					t.Fatal(err)
				}
				if !got.Rate.Equal(decimal.RequireFromString(expected[i])) || got.RateTS.After(at) {
					t.Errorf("at=%s rate=%s rate_ts=%s, want latest rate %s at-or-before requested time", at.Format(time.RFC3339), got.Rate, got.RateTS.Format(time.RFC3339), expected[i])
				}
			}
		})
	}
}

func TestConverter_NegativeCacheDoesNotHideLaterRate(t *testing.T) {
	registerFXTokens(t)
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	source := &fakeFXSource{points: []prices.PricePoint{
		{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: base.Add(30 * time.Second), Price: decimal.RequireFromString("1")},
	}}
	conv := NewTokenFXConverter(source, time.Minute, prices.SourceCoinGecko)
	_, err := conv.Convert(context.Background(), prices.Token("usdt"), prices.Currency("usd"), decimal.NewFromInt(100), base.Add(10*time.Second))
	if !errors.Is(err, ErrNoFXRate) {
		t.Fatalf("expected first request to have no rate, got %v", err)
	}
	_, err = conv.Convert(context.Background(), prices.Token("usdt"), prices.Currency("usd"), decimal.NewFromInt(100), base.Add(50*time.Second))
	if err != nil {
		t.Fatalf("later request must find the existing 12:00:30 rate, got %v", err)
	}
}
