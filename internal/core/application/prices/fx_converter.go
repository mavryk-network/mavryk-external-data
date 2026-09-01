package prices

import (
	"context"
	"strings"
	"sync"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fxCacheTTL is hardcoded: short enough that staleness never surprises, long
// enough that high QPS on one (token, currency) collapses to ~1 DB hit.
const fxCacheTTL = 60 * time.Second

// maxFXCacheEntries bounds the rate cache: keys partition by minute-bucket, so
// historical `?in=` ranges would otherwise leak one entry per bucket forever.
const maxFXCacheEntries = 50_000

// fxCacheSweepEvery triggers an opportunistic expired-entry sweep every N writes.
const fxCacheSweepEvery = 512

// FXHardStalenessCap is the age past which a rate reads as a dead feed and the
// conversion is refused outright (ErrNoFXRate) rather than served as fresh.
// Above 24h because deep CoinGecko history is daily-granularity. Deliberately a
// constant, not config: validation keeps fx_max_staleness_seconds below it, so
// the soft budget can never widen the dead-feed guard.
const FXHardStalenessCap = 26 * time.Hour

// HistoricalFXSource returns the freshest rate at or before `at`, so a
// historical chart converts at the rate current then rather than today's.
// ok=false when nothing exists at-or-before `at` (e.g. a date before the
// backfill window); the converter maps that to ErrNoFXRate.
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
	if strings.EqualFold(string(sourceToken), string(target)) {
		return ConversionResult{
			Amount:   sourceAmount,
			Rate:     decimal.NewFromInt(1),
			Source:   c.fxSource,
			RateTS:   ts,
			Identity: true,
		}, nil
	}

	// FX only exists in token_prices for registered tokens.
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

	// Staleness is measured against `ts`, so a historical lookup asks how
	// stale the feed was at that moment, not today.
	age := ts.Sub(rateTS)
	if age > FXHardStalenessCap {
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

// lookupRate reads the cache, then storage with at-or-before semantics. Keys
// collapse `ts` into minute buckets, so a burst within one minute costs a
// single upstream query per (token, currency).
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
	// A non-positive stored price (a CoinGecko glitch class) would become a
	// rate of 0 and silently zero every conversion. Negative-cache the miss so
	// a pre-backfill range costs one query per bucket, not a full scan.
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

// store writes under the lock and opportunistically evicts expired entries so
// the minute-bucketed key space cannot grow without bound.
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
