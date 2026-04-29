# ADR-0007: `CachedRepository` decorator over `Repository`

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

The pre-rewrite cache was a free-standing `responsecache.Cache` plus
ad-hoc cache logic duplicated in two `application/quotes/.../action.go`
files (each: "GetX → check cache → maybe repo.Call → maybe cache.Set").

Problems:
- Two places to update if TTL or key-shape changed.
- Actions had to know about the cache. Tests had to mock or pass-nil it.
- Switching to Redis later would mean editing every consumer of the cache.

## Decision

Implement caching as a **decorator** that satisfies the same `Repository`
interface as the underlying storage repository:

```go
type Repository interface {
    Save(ctx, points) (int64, error)
    Query(ctx, q) ([]PricePoint, error)
}

type CachedRepository struct { inner Repository; ttl time.Duration; ... }
// Save → write-through; invalidates affected (source, entity).
// Query → caches `IsLatest()` queries; passes time-window queries through.
```

`main.go` wires `apiprices.NewCachedRepository(realRepo, ttl)` once;
HTTP handlers and jobs see the decorated repository — no cache awareness
above this layer.

## Consequences

- ✅ One file owns TTL, key shape, eviction, and invalidation semantics.
- ✅ `ttl <= 0` disables caching transparently — handlers/jobs see no
  difference. Easy in tests and in environments where staleness matters.
- ✅ Save→invalidate is automatic. A live job that ingests a fresh price
  invalidates that (source, entity) on its way to the DB. Readers see
  their writes.
- ✅ Swapping in Redis later is a matter of writing a `RedisRepository`
  that satisfies `Repository` (or an `apiprices.RedisCache` decorator).
  No consumer changes.
- ⚠️ Range queries don't cache. We accept the read cost; range queries
  are rare and unbounded.
- ⚠️ Cache invalidation is conservative — any Save evicts the entire
  (source, entity) set, not just the affected metric. Cheap and simple;
  fine while the cache is in-process.
- 🔁 If we add multi-replica deployments and need shared cache, the
  decorator pattern accommodates Redis without changing call sites.

## Alternatives considered

- **Cache embedded in actions** (status quo). Rejected — duplication.
- **Cache embedded in repositories**. Couples persistence to caching;
  the repo would need a "no-cache" toggle for tests, recreating the
  problem we wanted to remove.
- **Read-through Redis with go-redis directly**. The right shape when
  we need shared cache, but premature today. Decorator pattern leaves
  this swap available.

## Notes

- Decorator: `internal/core/application/prices/cache.go`.
- Test: `internal/core/application/prices/cache_test.go` (TTL expiry,
  invalidation on save, concurrent reads/writes under `-race`).
