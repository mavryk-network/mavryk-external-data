package prices

import (
	"context"
	"sync"
	"time"

	"quotes/internal/core/domain/prices"
)

// CachedRepository wraps a Repository with a TTL cache for IsLatest queries.
// Time-window queries pass straight through (rare and unbounded — we never
// cache them).
//
// Cache invalidation: any Save() that returns rowsAffected > 0 invalidates the
// cache for the (Source, EntityKey) it wrote. This is conservative — readers
// see their writes immediately, at the cost of dropping a possibly-still-fresh
// entry. For our cadence (1–60 s ticks) this is the right tradeoff.
type CachedRepository struct {
	inner Repository
	ttl   time.Duration

	mu    sync.RWMutex
	cache map[cacheKey]cacheEntry
}

type cacheKey struct {
	Source  prices.Source
	Entity  string
	Metrics string // joined sorted metrics list ("" for all)
	Limit   int
}

type cacheEntry struct {
	expires time.Time
	points  []prices.PricePoint
}

// NewCachedRepository returns a decorator. ttl <= 0 disables caching transparently
// (the wrapper still satisfies Repository so callers don't branch on cache config).
func NewCachedRepository(inner Repository, ttl time.Duration) *CachedRepository {
	return &CachedRepository{
		inner: inner,
		ttl:   ttl,
		cache: make(map[cacheKey]cacheEntry),
	}
}

// Save writes through and invalidates affected entries.
func (c *CachedRepository) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	n, err := c.inner.Save(ctx, points)
	if err != nil {
		return n, err
	}
	if n > 0 && c.ttl > 0 {
		c.invalidate(points)
	}
	return n, nil
}

// Query consults the cache for IsLatest queries; otherwise passes through.
func (c *CachedRepository) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	if c.ttl <= 0 || !q.IsLatest() {
		return c.inner.Query(ctx, q)
	}
	key := keyFor(q)
	if cached, ok := c.lookup(key); ok {
		return cloneSlice(cached), nil
	}
	points, err := c.inner.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	c.store(key, cloneSlice(points))
	return points, nil
}

func (c *CachedRepository) lookup(k cacheKey) ([]prices.PricePoint, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[k]
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.points, true
}

func (c *CachedRepository) store(k cacheKey, points []prices.PricePoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[k] = cacheEntry{
		expires: time.Now().Add(c.ttl),
		points:  points,
	}
}

// invalidate drops every entry whose (source, entity) appears in points.
// Fast-path: collect distinct keys first, then a single locked sweep.
func (c *CachedRepository) invalidate(points []prices.PricePoint) {
	affected := make(map[struct {
		s prices.Source
		e string
	}]struct{}, 4)
	for _, p := range points {
		affected[struct {
			s prices.Source
			e string
		}{p.Source, p.EntityKey}] = struct{}{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if _, hit := affected[struct {
			s prices.Source
			e string
		}{k.Source, k.Entity}]; hit {
			delete(c.cache, k)
		}
	}
}

func keyFor(q prices.Query) cacheKey {
	return cacheKey{
		Source:  q.Source,
		Entity:  q.EntityKey,
		Metrics: joinSorted(q.Metrics),
		Limit:   q.Limit,
	}
}

func joinSorted(in []string) string {
	if len(in) == 0 {
		return ""
	}
	cp := make([]string, len(in))
	copy(cp, in)
	// Tiny n; insertion sort beats sort.Strings on small slices and avoids the import.
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	out := cp[0]
	for _, s := range cp[1:] {
		out += "|" + s
	}
	return out
}

func cloneSlice(in []prices.PricePoint) []prices.PricePoint {
	if in == nil {
		return nil
	}
	out := make([]prices.PricePoint, len(in))
	copy(out, in)
	return out
}
