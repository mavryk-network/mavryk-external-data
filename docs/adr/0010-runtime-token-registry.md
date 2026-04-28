# ADR-0010: Token registry loaded from DB at startup

- **Status**: Accepted
- **Date**: 2026-04-27
- **Deciders**: backend team

## Context

Pre-rewrite, supported tokens were hardcoded:

```go
const (
    TokenMVRK Token = "mvrk"
    TokenUSDT Token = "usdt"
)

func GetCoinGeckoID(token Token) string {
    switch token {
    case TokenMVRK: return "mavryk-network"
    case TokenUSDT: return "tether"
    }
}
```

Adding a new token meant editing Go code, regenerating, and redeploying.
Tokens, their CoinGecko IDs, and their decimals were duplicated between
this code and any consumer that needed them.

The greenfield rewrite introduced a `tokens` table seeded by migration:

```sql
CREATE TABLE tokens (symbol TEXT PRIMARY KEY, name, decimals, cg_id, enabled, …);
INSERT INTO tokens (symbol, name, decimals, cg_id, enabled) VALUES
    ('mvrk', 'Mavryk Network', 6, 'mavryk-network', TRUE),
    ('usdt', 'Tether',         6, 'tether',         TRUE);
```

The question was whether Go should still know about MVRK/USDT statically,
or load the registry at runtime.

## Decision

**Load at startup, validate at use.**

`main.go` queries `lookup.Tokens(ctx)` after the DB is up and calls
`prices.RegisterTokens(...)`. The `prices.NewToken(s)` constructor checks
the in-process registry. Hardcoded constants are gone.

```go
// main.go
tokens, _ := lookup.Tokens(bootstrapCtx)
prices.RegisterTokens(tokens)

// usage anywhere
t, err := prices.NewToken("mvrk")  // ok
t, err := prices.NewToken("xyz")   // error
```

## Consequences

- ✅ Adding a token = one INSERT (or migration row). No code change.
- ✅ Single source of truth for `decimals` and `cg_id` — read from DB,
  passed in `TokenInfo` to all consumers.
- ✅ Disabling a token (regulatory, deprecation, etc.) is a
  `UPDATE tokens SET enabled = false`. Live job filters; no deploy.
- ⚠️ Validation that depends on the registry happens *after* DB
  bootstrap, not at config-load. `validate.go` does syntactic checks
  (port ranges, durations); semantic checks (token X is registered)
  happen at job-start time. This is a deliberate separation —
  see [validate.go](../../internal/config/validate.go).
- ⚠️ If `tokens` is empty on startup (e.g. a wiped DB), live job logs
  `token_registry_empty_check_seed_migration` and runs no collectors.
  Operator-friendly; no silent "we're collecting nothing" state.
- 🔁 If we ever need hot-reload (add a token without restart), the
  registry interface (`RegisterTokens`) is already the right shape —
  add a poller or a NOTIFY-driven refresh.

## Alternatives considered

- **Hardcoded constants** (status quo). Rejected — re-deployment
  cost on every new token.
- **Registry in YAML** (config file). Couples deployment to Go-level
  registry; doesn't help operators.
- **External lookup service** (a separate registry microservice).
  Premature. Current scope is one DB.

## Notes

- Domain: `internal/core/domain/prices/token.go`
  (`Token`, `TokenInfo`, `RegisterTokens`, `NewToken`).
- Lookup: `internal/core/infrastructure/storage/repositories/lookup_repository.go`.
- Bootstrap: `cmd/quotes/main.go` (lines around `lookup.Tokens(bootstrapCtx)`).
