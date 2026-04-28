# Architecture Decision Records

Short, durable records of architectural choices made for `mavryk-external-data`.

## Why ADRs

Decisions written into code disappear into commit history; decisions written
into chat or PRs disappear faster. ADRs are versioned, reviewable artifacts
that explain *why* the system looks the way it does — especially the negative
space (alternatives that were considered and rejected).

## When to add one

- The decision will be expensive to reverse (storage schema, ORM choice, DI strategy).
- The decision is non-obvious from the code alone (e.g. _why two tables instead of one_).
- The decision is contested or has been re-litigated more than once.

Skip ADRs for trivial choices; the goal is signal, not paperwork.

## Format

Each ADR is `NNNN-short-title.md` and follows the [template](0000-template.md).
Number monotonically; never renumber existing files.

Status values:

- **Proposed** — under review.
- **Accepted** — current behaviour.
- **Superseded by ADR-NNNN** — replaced; keep the file for history.
- **Deprecated** — no longer relevant; not actively replaced.

## Index

| #    | Title | Status |
|------|-------|--------|
| [0001](0001-long-format-price-storage.md) | Long-format price storage in TimescaleDB | Accepted |
| [0002](0002-two-domain-tables-ft-vs-rwa.md) | Two domain tables (`token_prices`, `rwa_quote_prices`) | Accepted |
| [0003](0003-decimal-for-numeric-precision.md) | `shopspring/decimal` for monetary values | Accepted |
| [0004](0004-migration-tool-psql-loop.md) | Plain-SQL migrations via Makefile psql-loop | Accepted (revisit after 12 months) |
| [0005](0005-orm-keep-gorm.md) | Keep GORM (defer `sqlc` migration) | Accepted (revisit if SQL surface grows) |
| [0006](0006-generic-handler-wrap.md) | Generic `Wrap[Req, Res]` HTTP handler adapter | Accepted |
| [0007](0007-cache-decorator-pattern.md) | `CachedRepository` decorator over `Repository` | Accepted |
| [0008](0008-backfill-state-composite-key.md) | Composite `(source, entity_key)` backfill state | Accepted |
| [0009](0009-inbound-rate-limit-in-process.md) | In-process inbound rate limiter | Accepted |
| [0010](0010-runtime-token-registry.md) | Token registry loaded from DB at startup | Accepted |
| [0011](0011-openapi-3-hand-written.md) | Hand-written OpenAPI 3.0 spec; drop `swaggo/swag` | Accepted |
| [0012](0012-rwa-pair-discovery-and-normalization.md) | RWA pair discovery from Equiteez allowlist + price normalization via `tokens.decimals` | Accepted |

## Related

- [Upgrade plan](../../upgrade-plan.md) — the greenfield blueprint these ADRs codify.
- [Refactoring v2](../../refactoring_v2.md) — open follow-ups and anti-goals.
- [Working notes](../notes/) — design discussions that pre-dated the ADRs (informal).
