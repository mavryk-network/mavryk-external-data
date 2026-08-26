// Package cache provides a generic in-process TTL cache used by the
// per-domain CachedRepository decorators (prices, tickers, future kinds).
//
// The primitive intentionally stays small: get-or-load, invalidate-by-predicate,
// and a passive TTL fence on lookup. Invalidation policy is owned by the
// wrapping repository — see application/prices/cache.go and
// application/tickers/cache.go for the (source, entity) and (token) variants
// respectively.
//
// Zero TTL disables the cache transparently; lookups always miss, stores
// no-op. This keeps the wrapper construction unconditional and removes
// branchy "is cache enabled" checks from callers.
package cache

import (
	"context"
	"sync"
	"time"
)

// TTL is a thread-safe generic TTL cache keyed by string.
//
// When clone is non-nil it runs on every store AND every lookup return — use
// it to defend against caller aliasing for slice/map values. Pass nil when T
// is a value type (struct of decimals, snapshot) and aliasing isn't possible.
//
// Concurrent miss-loaders are NOT single-flighted: two parallel GetOrLoad on
// the same cold key both run load. Matches the existing prices cache
// semantics; if duplicate work becomes measurable, add a singleflight here in
// one place and every wrapper inherits it.
type TTL[T any] struct {
	ttl   time.Duration
	mu    sync.RWMutex
	store map[string]ttlEntry[T]
	clone func(T) T
	// gen counts invalidations. GetOrLoad snapshots it before running load and
	// stores only if unchanged — otherwise a reader that loaded pre-write data
	// could re-cache it right after a writer's invalidate, serving stale values
	// for a full TTL.
	gen uint64
}

type ttlEntry[T any] struct {
	expires time.Time
	value   T
}

// New builds a cache. clone may be nil. ttl <= 0 disables caching.
func New[T any](ttl time.Duration, clone func(T) T) *TTL[T] {
	return &TTL[T]{
		ttl:   ttl,
		store: make(map[string]ttlEntry[T]),
		clone: clone,
	}
}

// Enabled reports whether the cache is active (positive TTL).
func (c *TTL[T]) Enabled() bool { return c.ttl > 0 }

// Lookup returns (value, true) on a fresh hit. Expired entries miss but stay
// in the map until the next Invalidate sweep — cheap and bounded under
// realistic write rates.
func (c *TTL[T]) Lookup(key string) (T, bool) {
	var zero T
	if c.ttl <= 0 {
		return zero, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.store[key]
	if !ok || time.Now().After(e.expires) {
		return zero, false
	}
	if c.clone != nil {
		return c.clone(e.value), true
	}
	return e.value, true
}

// Store records (key, value). No-op when ttl <= 0.
func (c *TTL[T]) Store(key string, value T) {
	if c.ttl <= 0 {
		return
	}
	if c.clone != nil {
		value = c.clone(value)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[key] = ttlEntry[T]{
		expires: time.Now().Add(c.ttl),
		value:   value,
	}
}

// GetOrLoad returns the cached value on a hit; on a miss it calls load,
// stores the result on success, and returns. Errors are NOT cached — the
// next call retries. The store is skipped when an Invalidate/Purge ran while
// load was in flight (see TTL.gen).
func (c *TTL[T]) GetOrLoad(ctx context.Context, key string, load func(context.Context) (T, error)) (T, error) {
	if v, ok := c.Lookup(key); ok {
		return v, nil
	}
	c.mu.RLock()
	gen := c.gen
	c.mu.RUnlock()
	v, err := load(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.storeIfGen(key, v, gen)
	// Round-trip through Lookup to apply clone() consistently on the hot path.
	if c.clone != nil {
		return c.clone(v), nil
	}
	return v, nil
}

func (c *TTL[T]) storeIfGen(key string, value T, gen uint64) {
	if c.ttl <= 0 {
		return
	}
	if c.clone != nil {
		value = c.clone(value)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return
	}
	c.store[key] = ttlEntry[T]{
		expires: time.Now().Add(c.ttl),
		value:   value,
	}
}

// Invalidate drops every entry whose key matches the predicate. O(n) over
// the map; called from Save paths where n is bounded by ttl × write rate.
func (c *TTL[T]) Invalidate(match func(key string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for k := range c.store {
		if match(k) {
			delete(c.store, k)
		}
	}
}

// Purge drops every entry. Used by tests and by full-snapshot invalidation
// paths (e.g. tickers job tick — the job rewrites the entire snapshot, so
// matching on a specific (token) is wasteful when we can wipe).
func (c *TTL[T]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.store = make(map[string]ttlEntry[T])
}
