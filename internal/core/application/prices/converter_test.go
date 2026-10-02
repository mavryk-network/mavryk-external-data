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

// fakeFXSource is a minimal HistoricalFXSource for unit tests. Returns
// the freshest configured point with `ts <= at`, mimicking the real
// PK-index seek the production repo performs.
type fakeFXSource struct {
	mu      sync.Mutex
	points  []prices.PricePoint
	err     error
	queries int32
}

func (f *fakeFXSource) LatestRateAtOrBefore(
	_ context.Context,
	source prices.Source,
	tokenSymbol string,
	quoteCurrency string,
	at time.Time,
) (prices.PricePoint, bool, error) {
	atomic.AddInt32(&f.queries, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return prices.PricePoint{}, false, f.err
	}
	// Pick the freshest point matching (source, token, currency) with ts <= at.
	var picked *prices.PricePoint
	for i := range f.points {
		p := f.points[i]
		if p.Source != source || p.EntityKey != tokenSymbol || p.Metric != quoteCurrency {
			continue
		}
		if p.Timestamp.After(at) {
			continue
		}
		if picked == nil || p.Timestamp.After(picked.Timestamp) {
			picked = &f.points[i]
		}
	}
	if picked == nil {
		return prices.PricePoint{}, false, nil
	}
	return *picked, true, nil
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

// TestConverter_HistoricalAtOrBefore proves Decision #19 — the converter
// returns the rate at-or-before the requested ts, not "today's rate".
// This used to be a known prod bug (fix_todo.md, 2026-05-03) where every
// historical chart candle inherited today's FX rate.
func TestConverter_HistoricalAtOrBefore(t *testing.T) {
	registerFXTokens(t)
	t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: t1, Price: decimal.NewFromFloat(1.0001)},
			{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: t2, Price: decimal.NewFromFloat(1.0005)},
			{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: t3, Price: decimal.NewFromFloat(0.9999)},
		},
	}
	conv := NewTokenFXConverter(source, time.Hour, prices.SourceCoinGecko)

	cases := []struct {
		name     string
		askAt    time.Time
		wantRate decimal.Decimal
		wantTS   time.Time
		wantErr  error
	}{
		{"at t1 exactly", t1, decimal.NewFromFloat(1.0001), t1, nil},
		// Within the hard staleness cap the at-or-before rate is served
		// (flagged Stale past the soft budget).
		{"shortly after t1", t1.Add(20 * time.Hour), decimal.NewFromFloat(1.0001), t1, nil},
		// Past the hard cap a rate is a dead feed, not a coarse one — refuse
		// instead of converting a week-old rate as if current.
		{"mid-gap past hard cap", t1.Add(7 * 24 * time.Hour), decimal.Decimal{}, time.Time{}, ErrNoFXRate},
		{"at t2", t2, decimal.NewFromFloat(1.0005), t2, nil},
		{"after t3", t3.Add(time.Hour), decimal.NewFromFloat(0.9999), t3, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := conv.Convert(
				context.Background(),
				prices.Token("usdt"),
				prices.Currency("usd"),
				decimal.NewFromInt(100),
				c.askAt,
			)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("convert err = %v, want %v", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if !res.Rate.Equal(c.wantRate) {
				t.Errorf("rate = %s, want %s", res.Rate.String(), c.wantRate.String())
			}
			if !res.RateTS.Equal(c.wantTS) {
				t.Errorf("rate_ts = %v, want %v", res.RateTS, c.wantTS)
			}
		})
	}
}

// TestConverter_BeforeBackfill — asking for a rate before the earliest
// known row returns ErrNoFXRate, not today's rate.
func TestConverter_BeforeBackfill(t *testing.T) {
	registerFXTokens(t)
	source := &fakeFXSource{
		points: []prices.PricePoint{
			{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd",
				Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
				Price:     decimal.NewFromFloat(1.0)},
		},
	}
	conv := NewTokenFXConverter(source, time.Hour, prices.SourceCoinGecko)
	_, err := conv.Convert(
		context.Background(),
		prices.Token("usdt"),
		prices.Currency("usd"),
		decimal.NewFromInt(1),
		time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), // way before the fixture
	)
	if !errors.Is(err, ErrNoFXRate) {
		t.Errorf("err = %v, want ErrNoFXRate (no row at-or-before pre-backfill ts)", err)
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

// TestConverter_StaleReachableUpToHardCap pins the relationship config
// validation enforces: between the soft budget and the fixed hard cap a rate is
// SERVED and flagged stale; only past the cap is the conversion refused (and
// the currency dropped from the wire). A budget that swallowed the whole range
// would make fx.stale unobservable.
func TestConverter_StaleReachableUpToHardCap(t *testing.T) {
	registerFXTokens(t)
	// The widest budget config validation permits.
	budget := FXHardStalenessCap - time.Second

	cases := []struct {
		name      string
		age       time.Duration
		wantStale bool
		wantErr   bool
	}{
		{name: "inside the budget", age: budget - time.Hour, wantStale: false},
		{name: "between budget and hard cap", age: budget + 100*time.Millisecond, wantStale: true},
		{name: "past the hard cap", age: FXHardStalenessCap + time.Minute, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC()
			source := &fakeFXSource{points: []prices.PricePoint{{
				Source:    prices.SourceCoinGecko,
				EntityKey: "usdt",
				Timestamp: at.Add(-tc.age),
				Metric:    "usd",
				Price:     decimal.NewFromInt(2),
			}}}
			conv := NewTokenFXConverter(source, budget, prices.SourceCoinGecko)

			res, err := conv.Convert(context.Background(), prices.Token("usdt"),
				prices.Currency("usd"), decimal.NewFromInt(1), at)
			if tc.wantErr {
				if !errors.Is(err, ErrNoFXRate) {
					t.Fatalf("err = %v, want ErrNoFXRate", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if res.Stale != tc.wantStale {
				t.Errorf("Stale = %v, want %v for a rate %s old (budget %s, hard cap %s)",
					res.Stale, tc.wantStale, tc.age, budget, FXHardStalenessCap)
			}
		})
	}
}
