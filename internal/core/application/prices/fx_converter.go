package prices

import (
	"context"
	"strings"
	"sync"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fxCacheTTL is hardcoded — overrides via config aren't worth the surface
// area for a feature that only meaningfully interacts with cache when QPS
// is high. The TTL is short enough that staleness never appears as a
// surprise; long enough that 100 RPS on the same (token,currency) tuple
// collapses to ~1 DB hit.
const fxCacheTTL = 60 * time.Second

// LatestFXSource is the read interface tokenFXConverter needs from the
// FT-price storage. Defining it here (consumer-side) keeps the converter
// decoupled from the concrete repository — fakes are trivial in tests.
type LatestFXSource interface {
	Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error)
}

// NewTokenFXConverter wires a converter on top of the token_prices series.
// `maxStaleness == 0` falls back to 5 minutes (matches the
// `fx_max_staleness_seconds` config default).
func NewTokenFXConverter(repo LatestFXSource, maxStaleness time.Duration, fxSource prices.Source) PriceConverter {
	if maxStaleness <= 0 {
		maxStaleness = 5 * time.Minute
	}
	if fxSource == "" {
		fxSource = prices.SourceCoinGecko
	}
	return &tokenFXConverter{
		repo:         repo,
		maxStaleness: maxStaleness,
		fxSource:     fxSource,
		cache:        make(map[fxCacheKey]fxCacheEntry),
	}
}

type tokenFXConverter struct {
	repo         LatestFXSource
	maxStaleness time.Duration
	fxSource     prices.Source

	mu    sync.RWMutex
	cache map[fxCacheKey]fxCacheEntry
}

type fxCacheKey struct {
	Source     prices.Source
	Token      prices.Token
	Target     prices.Currency
	BucketUnix int64 // ts truncated to 60s; latest queries land in the same minute bucket
}

type fxCacheEntry struct {
	expires time.Time
	rate    decimal.Decimal
	rateTS  time.Time
}

// Convert implements PriceConverter.
func (c *tokenFXConverter) Convert(
	ctx context.Context,
	sourceToken prices.Token,
	target prices.Currency,
	sourceAmount decimal.Decimal,
	ts time.Time,
) (ConversionResult, error) {
	// Identity short-circuit — no upstream query.
	if strings.EqualFold(string(sourceToken), string(target)) {
		return ConversionResult{
			Amount:   sourceAmount,
			Rate:     decimal.NewFromInt(1),
			Source:   c.fxSource,
			RateTS:   ts,
			Identity: true,
		}, nil
	}

	// Source must be in the runtime token registry: the ft-side jobs only
	// write FX into token_prices for tokens that exist there.
	if _, ok := prices.LookupToken(sourceToken); !ok {
		return ConversionResult{}, ErrSourceTokenNotRegistered
	}
	if _, err := prices.NewCurrency(string(target)); err != nil {
		return ConversionResult{}, ErrUnsupportedTargetCurrency
	}

	rate, rateTS, err := c.lookupRate(ctx, sourceToken, target, ts)
	if err != nil {
		return ConversionResult{}, err
	}

	now := time.Now().UTC()
	stale := now.Sub(rateTS) > c.maxStaleness

	return ConversionResult{
		Amount: sourceAmount.Mul(rate),
		Rate:   rate,
		Source: c.fxSource,
		RateTS: rateTS,
		Stale:  stale,
	}, nil
}

// lookupRate consults the in-process cache then the storage layer. Cache
// keys collapse minute-buckets so 100 RPS within the same minute = 1
// upstream query. ts is the snapshot timestamp the caller is aligning to;
// for "latest" queries it's roughly `now`, so the bucket is the current
// minute.
func (c *tokenFXConverter) lookupRate(
	ctx context.Context,
	token prices.Token,
	target prices.Currency,
	ts time.Time,
) (decimal.Decimal, time.Time, error) {
	key := fxCacheKey{
		Source:     c.fxSource,
		Token:      token,
		Target:     target,
		BucketUnix: ts.Unix() / 60,
	}

	c.mu.RLock()
	entry, ok := c.cache[key]
	c.mu.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.rate, entry.rateTS, nil
	}

	q := prices.Query{
		Source:    c.fxSource,
		EntityKey: string(token),
		Metrics:   []string{string(target)},
		// Empty From/To ⇒ IsLatest ⇒ DISTINCT ON (currency) DESC.
	}
	points, err := c.repo.Query(ctx, q)
	if err != nil {
		return decimal.Decimal{}, time.Time{}, err
	}
	if len(points) == 0 {
		return decimal.Decimal{}, time.Time{}, ErrNoFXRate
	}
	// Pick the row matching our target metric; latest-mode yields one row
	// per currency, but be defensive about it.
	var picked *prices.PricePoint
	for i := range points {
		if strings.EqualFold(points[i].Metric, string(target)) {
			picked = &points[i]
			break
		}
	}
	if picked == nil {
		return decimal.Decimal{}, time.Time{}, ErrNoFXRate
	}

	c.mu.Lock()
	c.cache[key] = fxCacheEntry{
		expires: time.Now().Add(fxCacheTTL),
		rate:    picked.Price,
		rateTS:  picked.Timestamp,
	}
	c.mu.Unlock()
	return picked.Price, picked.Timestamp, nil
}
