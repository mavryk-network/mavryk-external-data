# ADR-0011: Hand-written OpenAPI 3.0 spec; drop `swaggo/swag`

- **Status**: Accepted (revisit if the spec drifts repeatedly)
- **Date**: 2026-04-28
- **Deciders**: backend team

## Context

Pre-rewrite the project used [`swaggo/swag`](https://github.com/swaggo/swag)
to generate Swagger 2.0 from `// @...` annotations. After the long-format
rewrite ([ADR-0001](0001-long-format-price-storage.md)) and the introduction
of the generic `Wrap[Req, Res]` adapter ([ADR-0006](0006-generic-handler-wrap.md)),
swaggo annotations stopped working: handler request/response types are now
**anonymous structs declared inside the method that returns `gin.HandlerFunc`**:

```go
func (d TokenPriceDeps) ListByToken() gin.HandlerFunc {
    type request  struct { Token string; ... }      // ← swag can't see this
    type pointDTO struct { Timestamp string; ... }   // ← swag can't see this
    bind   := func(c *gin.Context) (request, error) {...}
    action := func(ctx, req) ([]pointDTO, error) {...}
    return common.Wrap(bind, action)
}
```

`swag init` runs static AST analysis at the package level and refuses
function-scoped types. To keep swaggo we'd have to lift every Req/Res to
package level — undoing part of the boilerplate-kill the rewrite intended.

Other forces:

1. swaggo only produces **Swagger 2.0**. Modern API tooling (Redocly,
   Speakeasy, Stainless, Fern, modern API gateways) is OpenAPI 3.0.x.
2. swaggo pulls **6 transitive dependencies** (`KyleBanks/depth`,
   `go-openapi/spec`, `go-openapi/jsonpointer`/`jsonreference`/`swag`/...)
   for a build-time codegen tool that doesn't need to be in the binary.
3. The generated `docs/docs.go` was committed with `// DO NOT EDIT`; every
   API tweak produced a churned codegen diff in PRs.

## Decision

**Hand-write `docs/openapi.yaml`** as the canonical OpenAPI 3.0.3 spec.
Embed it into the binary via `go:embed`; serve `/openapi.yaml` and a
Swagger UI 5.x shell at `/docs`.

```
docs/
├── openapi.yaml       # source of truth (hand-edited)
├── index.html         # Swagger UI 5 shell (CDN-loaded)
└── embed.go           # go:embed wrapper
```

Drop `swaggo/files`, `swaggo/gin-swagger`, `swaggo/swag` from `go.mod`.
Replace the `make swagger` target with `make openapi-lint` (Redocly CLI
via `npx`).

## Consequences

- ✅ Spec is OpenAPI 3.0 — works with the modern tooling ecosystem.
- ✅ No code generation; no `// DO NOT EDIT` files in PR diffs.
- ✅ Six transitive deps removed from `go.mod` / `go.sum`.
- ✅ `Wrap[Req, Res]` and anonymous DTO types stay where they belong
  (inside the method that uses them).
- ✅ `docs/openapi.yaml` is reviewable by API consumers without reading Go.
- ⚠️ **Drift risk**: spec and code can diverge silently. Mitigated by
  - the OpenAPI examples in the spec being copy-pasted from real responses,
  - response-shape tests in `internal/core/api/http/handlers/*_test.go`,
  - manual review during PRs that touch handlers.
- ⚠️ Swagger UI shell loads `swagger-ui-dist@5.17.14` from `unpkg`; if the
  CDN is unreachable, `/docs` shows a blank page (the spec at `/openapi.yaml`
  still works directly). For air-gapped deployments, vendor the JS+CSS
  alongside the HTML and serve from `embed.FS`.
- 🔁 If drift becomes a real problem (more than one bug attributed to
  doc-vs-code mismatch), add a drift-test: in `httptest`, assert that
  recorded responses validate against the relevant `paths.X.responses` schema
  via `kin-openapi`. ~2 hours of work, can land any time.

## Alternatives considered

- **Keep swaggo, lift Req/Res to package-level** — ~half a day of work +
  ongoing 8 extra files of boilerplate. Reverses part of the §2.1
  cleanup. Rejected.
- **Keep swaggo, write annotations on dummy "doc shim" methods** — two
  parallel handler hierarchies. Confusing. Rejected.
- **Switch HTTP framework to `huma` or `goa`** — frameworks that generate
  spec from typed handlers. The right call for a spec-first greenfield
  project, but a multi-day rewrite of the gin layer that just got cleaned
  up. Defer.
- **Status quo (swaggo with empty annotations)** — what we had. The
  generated spec said `paths: {}`. Worse than no spec at all. Rejected.

## Notes

- Spec: [docs/openapi.yaml](../openapi.yaml).
- Embed wrapper: [docs/embed.go](../embed.go).
- HTTP handlers: `internal/core/api/http/handlers/openapi.go`.
- Routes: `/openapi.yaml`, `/docs`, `/docs/` —
  see [router.go](../../internal/core/api/http/router.go).
- Linting: `make openapi-lint` (Redocly CLI via npx).
- This decision closes [refactoring_v2 §1.3](../../refactoring_v2.md).
