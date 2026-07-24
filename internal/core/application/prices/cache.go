package prices

import (
	"context"
	"strconv"
	"strings"
	"time"

	"quotes/internal/core/application/cache"
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
//
// Built on top of cache.TTL[[]PricePoint] — see internal/core/application/cache
// for the generic primitive shared with the tickers domain.
type CachedRepository struct {
	inner Repository
	cache *cache.TTL[[]prices.PricePoint]
}

// NewCachedRepository returns a decorator. ttl <= 0 disables caching transparently
// (the wrapper still satisfies Repository so callers don't branch on cache config).
func NewCachedRepository(inner Repository, ttl time.Duration) *CachedRepository {
	return &CachedRepository{
		inner: inner,
		cache: cache.New(ttl, cloneSlice),
	}
}

// Save writes through and invalidates affected entries.
func (c *CachedRepository) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	n, err := c.inner.Save(ctx, points)
	if err != nil {
		return n, err
	}
	if n > 0 && c.cache.Enabled() {
		c.invalidate(points)
	}
	return n, nil
}

// Query consults the cache for IsLatest queries; otherwise passes through.
func (c *CachedRepository) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	if !c.cache.Enabled() || !q.IsLatest() {
		return c.inner.Query(ctx, q)
	}
	key := keyFor(q)
	return c.cache.GetOrLoad(ctx, key, func(ctx context.Context) ([]prices.PricePoint, error) {
		return c.inner.Query(ctx, q)
	})
}

// invalidate drops every cache entry whose (source, entity) appears in points.
// Cache keys encode (source, entity, metrics, limit) — we split on a sentinel
// to recover (source, entity) cheaply without holding a parsed-key index.
func (c *CachedRepository) invalidate(points []prices.PricePoint) {
	type key struct {
		source prices.Source
		entity string
	}
	affected := make(map[key]struct{}, 4)
	for _, p := range points {
		affected[key{p.Source, p.EntityKey}] = struct{}{}
	}
	c.cache.Invalidate(func(k string) bool {
		// key encoding: "src|entity|metrics|limit"; first two segments are
		// what we match on.
		parts := strings.SplitN(k, "|", 3)
		if len(parts) < 2 {
			return false
		}
		_, hit := affected[key{prices.Source(parts[0]), parts[1]}]
		return hit
	})
}

func keyFor(q prices.Query) string {
	// Order: source | entity | metrics-joined-sorted | limit.
	// strconv.Itoa (not the package itoa, which collapses >99 to "other" — it was
	// written for bounded metric labels): limits 100..10000 must not share a key,
	// or a cached N-row response could be served for a different ?limit.
	return string(q.Source) + "|" + q.EntityKey + "|" + joinSorted(q.Metrics) + "|" + strconv.Itoa(q.Limit)
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
		out += "," + s
	}
	return out
}

// itoa: see changes.go (same package). Reused to keep cache keys allocation-free.

func cloneSlice(in []prices.PricePoint) []prices.PricePoint {
	if in == nil {
		return nil
	}
	out := make([]prices.PricePoint, len(in))
	copy(out, in)
	return out
}
