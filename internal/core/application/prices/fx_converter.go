package prices

import (
	"context"
	"strings"
	"sync"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fxCacheTTL lets repeated historical lookups of the same instant share a query
// while allowing newly backfilled FX rows to become visible within a minute.
const fxCacheTTL = 60 * time.Second

// maxFXCacheEntries bounds the rate cache. Keys partition by (source, token,
// target, exact timestamp), so historical `?in=` ranges would otherwise
// accumulate one entry per requested instant forever (a slow memory leak
// under normal dashboard traffic). Expired entries are swept periodically and
// whenever the map grows past this cap.
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
	AtUnixNano int64 // exact requested instant; rates cannot leak across intra-minute changes
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

// lookupRate caches the exact requested instant. Sharing an arbitrary minute
// would return future rates to earlier callers, or hide a later rate behind an
// earlier negative lookup. Repeated historical requests still share one query.
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
		AtUnixNano: ts.UnixNano(),
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
	// range that predates the FX backfill costs one query per requested instant per
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
// entries so the historical key space cannot grow without bound.
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
		// Exact historical instants can all be live simultaneously. Expiry
		// sweeping alone cannot enforce the advertised memory bound.
		if len(c.cache) > maxFXCacheEntries {
			for k := range c.cache {
				delete(c.cache, k)
				if len(c.cache) <= maxFXCacheEntries-fxCacheSweepEvery {
					break
				}
			}
		}
	}
	c.mu.Unlock()
}
