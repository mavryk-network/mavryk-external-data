# ADR-0009: In-process inbound rate limiter

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

The service had outbound rate-limit on CoinGecko/Equiteez but nothing on
the inbound side. A single noisy client could exhaust the DB connection
pool, effectively DoS-ing every other consumer. Refactoring v2 §9.1
flagged this as a P0 gap.

We considered three places to enforce inbound throttling:

1. **In-process** middleware in this service.
2. **Redis-backed** rate limiter for cross-replica fairness.
3. **Edge gateway** (envoy / haproxy / cloud LB).

## Decision

**In-process per-IP** rate limiter, opt-in via config.

```yaml
server:
  rate_limit:
    rps: 100
    burst: 200
    per_ip: true
```

Implemented in `internal/core/api/http/middleware/rate_limit.go` using
`golang.org/x/time/rate`. Per-IP buckets stored in a sync-protected map;
idle entries (> 15 min) evicted by a background goroutine.

Disabled by default (`rps: 0`) — the service runs behind trusted
networks during initial rollout.

## Consequences

- ✅ Zero new infrastructure dependencies. Works the same in dev,
  staging, and the smallest prod footprint.
- ✅ Per-IP isolation: one abusive client doesn't affect others.
- ✅ Memory bounded by active-IP × bucket-size; eviction keeps it
  stable. We explicitly tested eviction under sustained load.
- ⚠️ Per-replica state: a client distributing requests across 5
  replicas gets 5× the configured RPS. Acceptable trade-off until
  we see real abuse patterns.
- ⚠️ Trusts `c.ClientIP()` — vulnerable to header spoofing if the
  service is exposed without a properly-configured proxy. Document
  in deployment guide.
- 🔁 Move to Redis-backed limiter when (a) we run >3 replicas behind
  a single LB and (b) per-replica drift becomes a real complaint.

## Alternatives considered

- **Redis-backed limiter** (e.g. `redis-rate`) — correct shape for
  multi-replica fairness, but adds a hard runtime dependency and
  another failure mode. Defer.
- **Edge gateway** (envoy/haproxy/cloud LB) — best for shared
  infrastructure but moves policy out of the service repo. We'd lose
  fast iteration on rules and the policy would couple to deployment
  topology.
- **No limit; trust the LB** — what we had. Fails on direct exposure
  and on shared-LB tenants.

## Notes

- Middleware: `internal/core/api/http/middleware/rate_limit.go`.
- Config: `internal/config/server.go` (`ServerRateLimitConfig`).
- Refused 429 responses include `{"code":"RATE_LIMITED","message":"Too many requests"}`
  for client-side handling.
