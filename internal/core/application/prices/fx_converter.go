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

// HistoricalFXSource returns the freshest FX rate row for
// (source, token, currency) whose timestamp is at or before `at`.
// Replaces the previous "always-latest" contract (Decision #19 in the
// price-change design doc / `fix_todo.md`). For latest-mode callers
// this is semantically equivalent — passing `time.Now()` as `at` still
// returns the freshest row. Historical callers (chart `?in=`,
// historical change conversions) get the at-or-before row their
// timestamp asks for, fixing the long-standing chart bug.
//
// Returns ok=false when no row exists at-or-before `at` (typical:
// asking for a date before backfill window). ErrNoFXRate is the
// converter-level mapping; the source itself reports ok=false.
type HistoricalFXSource interface {
	LatestRateAtOrBefore(
		ctx context.Context,
		source prices.Source,
		tokenSymbol string,
		quoteCurrency string,
		at time.Time,
	) (prices.PricePoint, bool, error)
}

// NewTokenFXConverter wires a converter on top of the token_prices series.
// `maxStaleness == 0` falls back to 5 minutes (matches the
// `fx_max_staleness_seconds` config default).
func NewTokenFXConverter(repo HistoricalFXSource, maxStaleness time.Duration, fxSource prices.Source) PriceConverter {
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
	repo         HistoricalFXSource
	maxStaleness time.Duration
	fxSource     prices.Source

	mu    sync.RWMutex
	cache map[fxCacheKey]fxCacheEntry
}

type fxCacheKey struct {
	Source     prices.Source
	Token      prices.Token
	Target     prices.Currency
	BucketUnix int64 // ts truncated to 60s; queries within the same minute share a slot
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

	// Staleness is measured against `ts` — the timestamp the caller asked
	// to convert against. For latest-mode (ts ≈ time.Now()) this is the
	// same as the old "now - rateTS" measurement. For historical lookups
	// (ts in the past) it's "how stale was the FX feed at the moment we're
	// asking about", which is the honest question.
	stale := ts.Sub(rateTS) > c.maxStaleness

	return ConversionResult{
		Amount: sourceAmount.Mul(rate),
		Rate:   rate,
		Source: c.fxSource,
		RateTS: rateTS,
		Stale:  stale,
	}, nil
}

// lookupRate consults the in-process cache then the storage layer using
// at-or-before semantics (Decision #19). Cache keys collapse minute-buckets
// of `ts` so 100 RPS within the same minute = 1 upstream query for the
// same (token, currency) tuple. Historical lookups (ts in the past)
// partition naturally into their own minute buckets.
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

	point, found, err := c.repo.LatestRateAtOrBefore(ctx, c.fxSource, string(token), string(target), ts)
	if err != nil {
		return decimal.Decimal{}, time.Time{}, err
	}
	if !found {
		return decimal.Decimal{}, time.Time{}, ErrNoFXRate
	}

	c.mu.Lock()
	c.cache[key] = fxCacheEntry{
		expires: time.Now().Add(fxCacheTTL),
		rate:    point.Price,
		rateTS:  point.Timestamp,
	}
	c.mu.Unlock()
	return point.Price, point.Timestamp, nil
}
