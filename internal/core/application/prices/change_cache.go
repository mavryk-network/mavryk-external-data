package prices

import (
	"sync"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// ChangeCache is a tiny in-memory TTL cache for the price-change endpoint.
// Per design Decision #1 it lives in its own file and does NOT generalise
// the existing CachedRepository (which couples invalidation to Save events
// on Repository.Query). The change endpoint reads aggregate-derived data
// with no Save event, so the simpler TTL-only model is correct.
//
// Per design Decision #2 the cache is per-anchor, not per-response: each
// (currency, period) pair is an independent entry, plus one entry per
// (currency) for "now". This way a request for ?periods=1h,30d does not
// evict the expensive 30d data when a paired 1h anchor expires.
//
// Currency is carried as a string (not prices.Currency) because the same
// cache serves FT (closed-enum currency codes like "usd") and RWA (open
// native-quote tickers like "usdt"). Per-class validation lives on the
// HTTP layer.
//
// Concurrency: sync.RWMutex over a single map keyed by struct literal.
// All entries are immutable once stored; callers must not mutate the
// returned decimals.
type ChangeCache struct {
	mu    sync.RWMutex
	items map[changeCacheKey]changeCacheEntry
}

// changeCacheKey identifies one cache slot. Period is the empty string
// when the slot holds the "now" value for (source, entity, currency).
type changeCacheKey struct {
	Source   prices.Source
	Entity   string
	Currency string
	Period   prices.Period
}

// changeCacheEntry is the stored payload. TS is the observation timestamp
// of the underlying price (used to compute response `as_of` / `from_ts`),
// not the cache write time. Expires is the cache write time + TTL.
type changeCacheEntry struct {
	Price   decimal.Decimal
	TS      time.Time
	Expires time.Time
}

// NewChangeCache constructs an empty cache.
func NewChangeCache() *ChangeCache {
	return &ChangeCache{items: make(map[changeCacheKey]changeCacheEntry)}
}

// TTLFor returns the cache TTL for a given period. The "now" slot
// (period = empty string) gets the shortest TTL because the underlying
// last(price) value advances on every CoinGecko / Equiteez tick.
//
//	now → 30s   1h → 30s   24h → 60s   7d → 5m   30d → 15m
//
// Anchors for longer periods are cheap to keep around because their
// underlying CA bucket close is immutable once the bucket finalises.
func TTLFor(p prices.Period) time.Duration {
	switch p {
	case prices.Period1h:
		return 30 * time.Second
	case prices.Period24h:
		return 60 * time.Second
	case prices.Period7d:
		return 5 * time.Minute
	case prices.Period30d:
		return 15 * time.Minute
	default:
		// Empty period = "now" slot. Same as 1h — short, since underlying
		// last(price) value advances on every tick.
		return 30 * time.Second
	}
}

// GetNow returns the cached "now" entry for (source, entity, currency),
// or ok=false on miss / expiry.
func (c *ChangeCache) GetNow(source prices.Source, entity, currency string) (price decimal.Decimal, ts time.Time, ok bool) {
	return c.get(changeCacheKey{Source: source, Entity: entity, Currency: currency, Period: ""})
}

// GetAnchor returns the cached anchor entry for (source, entity, currency, period),
// or ok=false on miss / expiry.
func (c *ChangeCache) GetAnchor(source prices.Source, entity, currency string, period prices.Period) (price decimal.Decimal, ts time.Time, ok bool) {
	return c.get(changeCacheKey{Source: source, Entity: entity, Currency: currency, Period: period})
}

// SetNow stores a "now" entry with the period-derived TTL.
func (c *ChangeCache) SetNow(source prices.Source, entity, currency string, price decimal.Decimal, ts time.Time) {
	c.set(changeCacheKey{Source: source, Entity: entity, Currency: currency, Period: ""}, price, ts, TTLFor(""))
}

// SetAnchor stores an anchor entry with the period-derived TTL.
func (c *ChangeCache) SetAnchor(source prices.Source, entity, currency string, period prices.Period, price decimal.Decimal, ts time.Time) {
	c.set(changeCacheKey{Source: source, Entity: entity, Currency: currency, Period: period}, price, ts, TTLFor(period))
}

func (c *ChangeCache) get(k changeCacheKey) (decimal.Decimal, time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[k]
	if !ok || time.Now().After(e.Expires) {
		return decimal.Decimal{}, time.Time{}, false
	}
	return e.Price, e.TS, true
}

func (c *ChangeCache) set(k changeCacheKey, price decimal.Decimal, ts time.Time, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[k] = changeCacheEntry{Price: price, TS: ts, Expires: time.Now().Add(ttl)}
}

// Len returns the number of live entries (including expired-but-not-evicted).
// Test-only helper; production code should not depend on this.
func (c *ChangeCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}
