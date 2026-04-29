# ADR-0006: Generic `Wrap[Req, Res]` HTTP handler adapter

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

Pre-rewrite, every endpoint was four files of mostly identical glue:

```
http/quotes/get_all/handler.go     (~50 LOC)
http/quotes/get_by_token/handler.go (~60 LOC)
application/quotes/get_all/action.go (~25 LOC)
application/quotes/get_by_token/action.go (~45 LOC)
```

The handler files all looked like `bind → action → switch{err}`. The
action files were thin wrappers around a single repository call. Adding
RWA endpoints would have meant six more such files of nothing.

## Decision

A single generic adapter in `internal/core/api/http/common/handler.go`:

```go
func Wrap[Req any, Res any](
    bind   func(*gin.Context) (Req, error),
    action func(context.Context, Req) (Res, error),
) gin.HandlerFunc
```

Wrap composes a binder and an action, maps domain errors
(`prices.ErrTokenNotFound` → 404) to HTTP statuses, and JSON-encodes the
result. Each handler is now ~30 LOC of binder + action; the boilerplate
disappeared.

## Consequences

- ✅ Adding an endpoint is one file with two closures. No new
  package directories, no error-mapping copy-paste.
- ✅ Error mapping is centralized in `mapDomainError` — sentinel
  errors propagate from binder *or* action, both via the same path.
- ✅ Binders and actions are individually testable as plain functions
  (no `gin.Context` plumbing in the action).
- ⚠️ Generics noise: callers see `Wrap[Req, Res]` in route registration.
  The cost is minor (Go's inference picks Req/Res in most cases).
- ⚠️ All handlers share the same response shape (`200 + JSON(res)` or
  `mapDomainError`). If we ever need streaming or non-JSON responses,
  those endpoints opt out of `Wrap` and roll their own.
- 🔁 If response-envelope semantics change ([refactoring_v2 §1.1](../../refactoring_v2.md)),
  `Wrap` is the single point of change.

## Alternatives considered

- **`huma`, `echo` middlewares, etc.** — third-party generic-handler
  packages. We didn't want a framework dependency for what's
  effectively 50 lines of code.
- **Code generation** (gRPC-style) — overkill at our endpoint count.
- **Status quo** — pre-rewrite four-file pattern. Rejected; it was the
  bulk of the boilerplate-tax this refactor was about removing.

## Notes

- Adapter: `internal/core/api/http/common/handler.go`.
- Example handlers: `internal/core/api/http/handlers/{token_prices,rwa_prices}.go`.
- Tests: `internal/core/api/http/handlers/{token_prices,rwa_prices}_test.go`
  exercise the bind/action layers via `httptest`.
