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

// maxFXCacheEntries bounds the rate cache. Keys partition by (source, token,
// target, minute-bucket-of-ts), so historical `?in=` ranges would otherwise
// accumulate one immortal entry per minute-bucket forever (a slow memory leak
// under normal dashboard traffic). Expired entries are swept periodically and
// whenever the map grows past this cap.
const maxFXCacheEntries = 50_000

// fxCacheSweepEvery triggers an opportunistic expired-entry sweep every N writes.
const fxCacheSweepEvery = 512

// fxHardStalenessCap is where a coarse rate becomes a dead feed: past this age
// the conversion is refused (ErrNoFXRate → dropped from the wire) instead of
// served as if fresh. Sits above 24h because deep CoinGecko history is
// daily-granularity, so legitimate historical lookups can trail `ts` by up to
// a day regardless of the soft fx_max_staleness budget.
const fxHardStalenessCap = 26 * time.Hour

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

	mu     sync.RWMutex
	cache  map[fxCacheKey]fxCacheEntry
	writes int // write counter driving the opportunistic sweep
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
	found   bool // false = negative cache (no usable rate at-or-before ts)
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
	age := ts.Sub(rateTS)
	if age > fxHardStalenessCap {
		return ConversionResult{}, ErrNoFXRate
	}
	stale := age > c.maxStaleness

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
		if !entry.found {
			return decimal.Decimal{}, time.Time{}, ErrNoFXRate
		}
		return entry.rate, entry.rateTS, nil
	}

	point, found, err := c.repo.LatestRateAtOrBefore(ctx, c.fxSource, string(token), string(target), ts)
	if err != nil {
		return decimal.Decimal{}, time.Time{}, err
	}
	// Treat missing OR non-positive rates as "no rate". A zero/negative stored
	// price (a known CoinGecko glitch class) would otherwise become an FX rate of
	// 0 and silently zero out every ?in= conversion. Negative-cache the miss so a
	// range that predates the FX backfill costs one query per minute-bucket per
	// TTL instead of a full ~10k-query scan on every request.
	if !found || !point.Price.IsPositive() {
		c.store(key, fxCacheEntry{expires: time.Now().Add(fxCacheTTL), found: false})
		return decimal.Decimal{}, time.Time{}, ErrNoFXRate
	}

	c.store(key, fxCacheEntry{
		expires: time.Now().Add(fxCacheTTL),
		rate:    point.Price,
		rateTS:  point.Timestamp,
		found:   true,
	})
	return point.Price, point.Timestamp, nil
}

// store writes an entry under the write lock and opportunistically evicts expired
// entries so the minute-bucketed key space cannot grow without bound.
func (c *tokenFXConverter) store(key fxCacheKey, entry fxCacheEntry) {
	c.mu.Lock()
	c.cache[key] = entry
	c.writes++
	if c.writes%fxCacheSweepEvery == 0 || len(c.cache) > maxFXCacheEntries {
		now := time.Now()
		for k, e := range c.cache {
			if !now.Before(e.expires) {
				delete(c.cache, k)
			}
		}
	}
	c.mu.Unlock()
}
