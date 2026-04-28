package prices

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fakeFXSource is a minimal LatestFXSource for unit tests. Each Query
// returns the configured points and increments hit counter so we can
// observe cache effectiveness.
type fakeFXSource struct {
	mu      sync.Mutex
	points  []prices.PricePoint
	err     error
	queries int32
}

func (f *fakeFXSource) Query(_ context.Context, _ prices.Query) ([]prices.PricePoint, error) {
	atomic.AddInt32(&f.queries, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.points, nil
}

func registerFXTokens(t *testing.T) {
	t.Helper()
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
		{Symbol: "mvrk", Name: "Mavryk", Decimals: 6, Enabled: true},
	})
}

func TestConverter_Identity(t *testing.T) {
	registerFXTokens(t)
	conv := NewTokenFXConverter(&fakeFXSource{}, time.Minute, prices.SourceCoinGecko)

	res, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("usdt"), // not really a currency, but case-insensitive identity check
		decimal.NewFromInt(100),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	if !res.Identity {
		t.Errorf("Identity = false, want true")
	}
	if !res.Rate.Equal(decimal.NewFromInt(1)) {
		t.Errorf("Rate = %s, want 1", res.Rate.String())
	}
	if !res.Amount.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Amount = %s, want 100", res.Amount.String())
	}
}

func TestConverter_Success(t *testing.T) {
	registerFXTokens(t)
	rateTS := time.Now().Add(-30 * time.Second).UTC()
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{
				Source:    prices.SourceCoinGecko,
				EntityKey: "usdt",
				Timestamp: rateTS,
				Metric:    "usd",
				Price:     decimal.NewFromFloat(1.0001),
			},
		},
	}
	conv := NewTokenFXConverter(source, 5*time.Minute, prices.SourceCoinGecko)

	res, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("usd"),
		decimal.NewFromFloat(56.25),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if res.Identity {
		t.Errorf("Identity should be false for usdt→usd")
	}
	want := decimal.NewFromFloat(56.25).Mul(decimal.NewFromFloat(1.0001))
	if !res.Amount.Equal(want) {
		t.Errorf("Amount = %s, want %s", res.Amount.String(), want.String())
	}
	if !res.Rate.Equal(decimal.NewFromFloat(1.0001)) {
		t.Errorf("Rate = %s", res.Rate.String())
	}
	if res.Stale {
		t.Errorf("Stale = true, but rate is 30s old (budget = 5m)")
	}
}

func TestConverter_NoRate(t *testing.T) {
	registerFXTokens(t)
	conv := NewTokenFXConverter(&fakeFXSource{points: nil}, time.Minute, prices.SourceCoinGecko)
	_, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("usd"),
		decimal.NewFromInt(1),
		time.Now(),
	)
	if !errors.Is(err, ErrNoFXRate) {
		t.Errorf("err = %v, want ErrNoFXRate", err)
	}
}

func TestConverter_StaleRate(t *testing.T) {
	registerFXTokens(t)
	rateTS := time.Now().Add(-10 * time.Minute).UTC() // older than 5m budget
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{
				Source:    prices.SourceCoinGecko,
				EntityKey: "usdt",
				Timestamp: rateTS,
				Metric:    "usd",
				Price:     decimal.NewFromFloat(1.0),
			},
		},
	}
	conv := NewTokenFXConverter(source, 5*time.Minute, prices.SourceCoinGecko)

	res, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("usd"),
		decimal.NewFromInt(100),
		time.Now(),
	)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !res.Stale {
		t.Errorf("Stale = false, want true (rate is 10m old)")
	}
}

func TestConverter_UnregisteredSourceToken(t *testing.T) {
	registerFXTokens(t)
	conv := NewTokenFXConverter(&fakeFXSource{}, time.Minute, prices.SourceCoinGecko)
	// "eurc" is not in the test registry — should fail without hitting source.
	_, err := conv.Convert(
		context.Background(),
		prices.Token("eurc"),
		prices.Currency("usd"),
		decimal.NewFromInt(1),
		time.Now(),
	)
	if !errors.Is(err, ErrSourceTokenNotRegistered) {
		t.Errorf("err = %v, want ErrSourceTokenNotRegistered", err)
	}
}

func TestConverter_UnsupportedTarget(t *testing.T) {
	registerFXTokens(t)
	conv := NewTokenFXConverter(&fakeFXSource{}, time.Minute, prices.SourceCoinGecko)
	_, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("xyz"),
		decimal.NewFromInt(1),
		time.Now(),
	)
	if !errors.Is(err, ErrUnsupportedTargetCurrency) {
		t.Errorf("err = %v, want ErrUnsupportedTargetCurrency", err)
	}
}

func TestConverter_CacheHit(t *testing.T) {
	registerFXTokens(t)
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{
				Source:    prices.SourceCoinGecko,
				EntityKey: "usdt",
				Timestamp: time.Now().UTC(),
				Metric:    "usd",
				Price:     decimal.NewFromFloat(1.0),
			},
		},
	}
	conv := NewTokenFXConverter(source, time.Minute, prices.SourceCoinGecko)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_, _ = conv.Convert(
			context.Background(),
			prices.Token("usdt"),
			prices.Currency("usd"),
			decimal.NewFromInt(int64(i+1)),
			now,
		)
	}
	if got := atomic.LoadInt32(&source.queries); got != 1 {
		t.Errorf("upstream queries = %d, want 1 (5 calls in same minute bucket)", got)
	}
}

func TestConverter_ConcurrentSafe(t *testing.T) {
	registerFXTokens(t)
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{
				Source:    prices.SourceCoinGecko,
				EntityKey: "usdt",
				Timestamp: time.Now().UTC(),
				Metric:    "usd",
				Price:     decimal.NewFromFloat(1.0),
			},
		},
	}
	conv := NewTokenFXConverter(source, time.Minute, prices.SourceCoinGecko)
	now := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = conv.Convert(
					context.Background(),
					prices.Token("usdt"),
					prices.Currency("usd"),
					decimal.NewFromInt(1),
					now,
				)
			}
		}()
	}
	wg.Wait()
	// Smoke: concurrent calls don't panic and don't blow up the upstream
	// counter. Exact value can vary because of cache miss racing on first
	// hit, but it must be much less than 50*20 = 1000.
	if got := atomic.LoadInt32(&source.queries); got > 50 {
		t.Errorf("upstream queries = %d under heavy concurrency, expected ~1-3", got)
	}
}
