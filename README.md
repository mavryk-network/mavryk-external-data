# Mavryk External Data

Go service that collects and serves cryptocurrency quotes (FT prices via
CoinGecko, RWA orderbook prices via the Equiteez indexer). Long-format storage
on TimescaleDB, generic HTTP layer, decorator-based caching.

## Why this exists

Two upstream sources, three jobs, one API:

- **CoinGecko live** — minute-cadence price collection for fungible tokens
  (MVRK, USDT) in N quote currencies (USD, EUR, BTC, …).
- **CoinGecko backfill** — reverse-walk over historical CoinGecko data to
  populate `token_prices` from a configured `start_from` to "now."
- **Equiteez RWA collector** — polls Equiteez orderbooks for known
  `rwa_pairs` and writes bid/ask/last to `rwa_quote_prices`.
  Pair discovery is **automatic**: on startup the service syncs
  `rwa_pairs` against Equiteez `in_allowlist=true` tokens/orderbooks —
  no manual seed required. Prices are normalized by the quote
  currency's decimals from the `tokens` registry (so micro-USDT
  `56_250_000` is stored as `56.25`). See
  [ADR-0012](docs/adr/0012-rwa-pair-discovery-and-normalization.md).

Consumers read everything through one HTTP API.

## Architecture at a glance

```
                        ┌──────────────────────┐
              CoinGecko ┤ live job     → token_prices
              CoinGecko ┤ backfill job → token_prices
              CoinGecko ┤ tickers job  → exchanges + token_tickers
                        └──────────────────────┘
                        ┌──────────────────────┐
                Equiteez┤ RWA job → rwa_quote_prices
                        └──────────────────────┘

                                      │
                          CachedRepository (TTL, in-process)
                                      │
                                ┌─────┴──────┐
                                │  HTTP API  │
                                │ /v1/prices │
                                │ /v1/rwa    │
                                │ /v1/tickers│
                                └────────────┘
```

Long-format hypertables, single source of truth for tokens (`tokens` table),
each table reused across FT and RWA via the `Repository` interface. Adding a
new currency, source, or metric requires no schema change.

For the *why* of every architectural decision, see the [ADRs](docs/adr/).
For the greenfield blueprint that produced this layout, see
[upgrade-plan.md](upgrade-plan.md).

## Repository layout

```
mavryk-external-data/
├── cmd/quotes/                     # main()
├── migrations/                     # plain-SQL migrations (lex-ordered)
├── internal/
│   ├── config/                    # YAML + env, validation, defaults
│   ├── core/
│   │   ├── domain/prices/         # PricePoint, Source, Token, Currency, Side
│   │   ├── domain/tickers/        # Ticker, Snapshot, Distribution, ExchangeKind
│   │   ├── application/cache/     # Generic TTL[T any] — backs all CachedRepositories
│   │   ├── application/prices/    # Repository interface, CachedRepository decorator
│   │   ├── application/tickers/   # Repository + 2-TTL CachedRepository (latest/distribution)
│   │   ├── api/http/              # Gin app, generic Wrap[Req,Res], handlers
│   │   └── infrastructure/
│   │       ├── interactions/      # CoinGecko client (prices, tickers), Equiteez client
│   │       ├── httpclient/        # Resilience: retry, CB, rate-limit, transport
│   │       ├── jobs/              # live, backfill, RWA, tickers collectors
│   │       └── storage/           # entities, repositories, db.go
│   ├── logging/                   # zerolog setup, request_id middleware
│   └── metrics/                   # Prometheus collectors
├── docs/
│   ├── adr/                       # Architecture decision records
│   └── notes/                     # Pre-decision design discussions
├── upgrade-plan.md
├── refactoring_v2.md
└── README.md  ← you are here
```

## API

| Endpoint                          | Description                                          |
|-----------------------------------|------------------------------------------------------|
| `GET /healthz`                    | Liveness — process up                                |
| `GET /readyz`                     | Readiness — DB ping + drain gate                     |
| `GET /metrics`                    | Prometheus exposition                                |
| `GET /v1/prices/:token`           | FT price points (range or latest)                    |
| `GET /v1/prices/:token/latest`    | FT snapshot — all currencies, transposed             |
| `GET /v1/prices/:token/count`     | Total row count for token                            |
| `GET /v1/rwa/:symbol`             | RWA orderbook points (range or latest); `:symbol` is `{base}-{quote}`, case-insensitive (e.g. `mars1-usdt`) |
| `GET /v1/rwa/:symbol/latest`      | RWA snapshot — all sides, transposed                 |
| `GET /v1/tickers/:token/latest`        | Per-exchange tickers for token: price, 24h volume, 1D change %, exchange logo. Filters stale rows by default; `?include_stale=true` opts in |
| `GET /v1/tickers/:token/distribution`  | Volume-distribution pie data; `?group_by=exchange\|target&in=usd` aggregates volume into one row per group with `share_pct` |

### Common query parameters

| Param           | On endpoints                | Notes                                       |
|-----------------|-----------------------------|---------------------------------------------|
| `from`          | list                        | RFC3339; both omitted → "latest" mode       |
| `to`            | list                        | RFC3339                                     |
| `limit`         | list                        | capped by `server.max_query_limit` (10000)  |
| `currency`      | FT list                     | filter: `usd`, `usd,eur`                    |
| `side`          | RWA list                    | filter: `bid`, `ask`, `last`, `mid`         |
| `in`            | RWA + tickers list+latest   | read-side conversion targets, e.g. `usd,eur,aed` (see [ADR-0013](docs/adr/0013-multi-currency-rwa-conversion-read-side.md)); capped by `server.max_in_currencies` (10) |
| `include_stale` | tickers /latest             | `true` returns rows older than `server.ticker_stale_after` (default 1h) flagged `is_stale: true` |
| `group_by`      | tickers /distribution       | `exchange` or `target` — required          |

### Examples

```bash
# Latest USD price for MVRK
curl http://localhost:3010/v1/prices/mvrk/latest

# MVRK in USD over a window
curl "http://localhost:3010/v1/prices/mvrk?currency=usd&from=2026-04-01T00:00:00Z&to=2026-04-02T00:00:00Z"

# Per-exchange tickers for MVRK with USD+EUR conversions
curl "http://localhost:3010/v1/tickers/mvrk/latest?in=usd,eur"

# Pie chart: volume share by exchange
curl "http://localhost:3010/v1/tickers/mvrk/distribution?group_by=exchange&in=usd"

# Pie chart: volume share by quote currency (BTC vs USDT vs ETH ...)
curl "http://localhost:3010/v1/tickers/mvrk/distribution?group_by=target&in=usd"

# RWA pair 42 — last bid/ask in native quote (USDT)
curl http://localhost:3010/v1/rwa/42/latest

# RWA pair 42 — same snapshot rendered into USD / EUR / AED via FT-side FX
curl "http://localhost:3010/v1/rwa/42/latest?in=usd,eur,aed"
# →
# {
#   "source": "equiteez", "entity": "42", "timestamp": "...",
#   "native_quote": "usdt",
#   "values": { "bid": "100.42", "ask": "100.50", "last": "100.45" },
#   "in": {
#     "usd": { "values": { "bid": "100.43", ... }, "fx": { "rate": "1.0001", "source": "coingecko", "ts": "...", "method": "rate" } },
#     "eur": { "fx": { "error": "no_fx_rate" } }   ← partial success, still 200
#   }
# }
```

### Response shapes

`GET /v1/prices/:token` — list of points (long-format):

```json
[
  {"timestamp": "2026-04-27T12:00:00Z", "currency": "usd", "price": "0.0715412"},
  {"timestamp": "2026-04-27T12:00:00Z", "currency": "eur", "price": "0.06094094"}
]
```

`GET /v1/prices/:token/latest` — transposed snapshot:

```json
{
  "source": "coingecko",
  "entity": "mvrk",
  "timestamp": "2026-04-27T12:00:00Z",
  "values": {"usd": "0.0715412", "eur": "0.06094094"}
}
```

Prices are JSON strings (per [ADR-0003](docs/adr/0003-decimal-for-numeric-precision.md))
to preserve full `numeric(38,18)` precision on round-trip.

Errors use a stable envelope:

```json
{"code": "NOT_FOUND", "message": "Token not found"}
```

Codes: `INVALID_ARGUMENT` (400), `NOT_FOUND` (404), `UNAVAILABLE` (503),
`RATE_LIMITED` (429), `INTERNAL` (500).

## Configuration

The service reads `config.yaml` and overrides with environment variables.
Both files are commented in detail; see also [.env.example](.env.example).

Key knobs:

```yaml
server:
  port: "3010"
  max_query_limit: 10000          # hard cap on ?limit=
  handler_timeout: "10s"          # per-request ctx budget
  shutdown_drain_seconds: 5       # /readyz drain before HTTP shutdown
  rate_limit:                     # inbound per-IP throttle (0=disabled)
    rps: 100
    per_ip: true

database:
  max_open_conns: 25
  max_idle_conns: 5
  conn_max_lifetime: "30m"
  batch_size: 500                 # Save() chunk size

coingecko:
  rate_limit:                     # outbound (shared registry)
    rps: 8

backfill:
  enabled: false
  start_from: "2025-09-18T00:00:00Z"
  tick_seconds: 5
  jitter_ms: 1000
  chunk_minutes: 360
  max_backoff_ms: 86400000        # 24h hard cap

rwa:
  enabled: false
  interval_seconds: 60
  concurrency: 4
```

Token registry lives in the `tokens` table (loaded at startup;
[ADR-0010](docs/adr/0010-runtime-token-registry.md)). Per-token live/backfill
overrides go in `config.yaml` under `tokens:`; see [config.yaml](config.yaml).

## Running

### Prerequisites

- Go 1.24+
- PostgreSQL 15+ with TimescaleDB extension (the docker-compose stack uses
  `timescale/timescaledb:latest-pg16`)
- `psql` for the migration loop

### Local

```bash
go mod tidy
make migrate-up           # applies migrations/*.sql in lex order
go run cmd/quotes/main.go
```

The service listens on `:3010` by default.

### Docker

```bash
docker-compose up -d      # postgres + migrations + app
docker-compose logs -f app
```

The compose stack pins TimescaleDB and runs migrations as a one-shot service
before starting the app.

## Migrations

Plain-SQL files in `migrations/` applied in lexicographic order
([ADR-0004](docs/adr/0004-migration-tool-psql-loop.md)). Idempotent — safe
to re-run; no down-migrations.

```bash
make migrate-up
```

For Docker, the `migration` stage in the Dockerfile runs
`scripts/run-migrations.sh`.

Schema highlights:

- `sources`, `tokens`, `rwa_pairs`, `exchanges` — lookup tables (FK targets).
- `token_prices` — FT hypertable (`(token_symbol, source_code, quote_currency, ts)` PK).
- `rwa_quote_prices` — RWA hypertable (`(pair_id, side, ts)` PK).
- `token_tickers` — per-exchange ticker hypertable
  (`(token_symbol, source_code, exchange_id, target_symbol, ts)` PK).
- `backfill_state` — composite `(source_code, entity_key)` cursor +
  error/backoff bookkeeping ([ADR-0008](docs/adr/0008-backfill-state-composite-key.md)).
- Continuous aggregates: `token_prices_1h`, `_1d`, `rwa_quote_prices_1h`.
- Compression policies: 14d for FT, 7d for RWA, 7d for tickers. Retention:
  2 years on FT/RWA raw, 90d on tickers raw (no validated retro-analytics
  use yet — extend when a consumer asks).

## Observability

### Metrics (`/metrics`)

- `http_*` — request counts, latency histograms, response classes.
- `outbound_http_*` — retries, CB transitions, CB current state, RL wait.
- `job_tick_duration_seconds`, `job_errors_total`, `job_rows_affected_total`.
- `backfill_oldest_ts_seconds` (gauge), `backfill_auto_disabled_total`.
- `db_pool_*` — open / in-use / idle / wait-duration.

### Logs

Structured JSON via zerolog. Toggle level with `LOG_LEVEL=debug`. The
`logging_initialized` line is emitted once at startup with the active
level and caller-annotation flag (`LOG_CALLER=false` to disable).

Outbound HTTP logs carry `request_id` when triggered from an HTTP handler;
background-job logs carry `tick_id` for per-tick correlation.

### Profiling

`server.pprof_enabled: true` mounts `/debug/pprof/*`. **Bind to a private
interface in production** — pprof endpoints disclose stack traces.

## Testing

```bash
go test ./...                                  # fast
go test -race -timeout 5m -cover ./...         # what CI runs
```

Coverage today:

- Domain types (Currency/Side/Token, Snapshot transpose).
- Cache decorator (TTL expiry, save-invalidation, concurrency under `-race`).
- CoinGecko mapper (sort order, malformed input, multi-currency).
- Backfill `chunkBounds`, `computeBackoff`, `parseBackfillTime`, `truncateError`.
- HTTP handlers (`httptest`): bind/error mapping, 404 on unknown token, 400
  on bad currency/limit, transposed-snapshot shape.
- Rate-limit `Normalized()` and `Enabled()`.

Open follow-ups (integration tests with `testcontainers-go`, full
`stepToken` via mocked dependencies) are tracked in
[refactoring_v2.md](refactoring_v2.md).

## CI

`.github/workflows/main.yml`:

- `govulncheck` against the dependency tree.
- `golangci-lint`.
- `go test -race -coverprofile=coverage.out -covermode=atomic ./...`
  with coverage uploaded as an artifact.
- `go build -trimpath -ldflags "-w -s"` of the binary.
- On tag pushes: multi-stage Docker image build + push + ArgoCD sync.

## Documentation

- [docs/adr/](docs/adr/) — Architecture decision records (10 ADRs covering
  storage shape, ORM choice, migration tool, generic-handler, caching, etc.).
- [docs/notes/](docs/notes/) — Working-design notes and the integration guide.
- [upgrade-plan.md](upgrade-plan.md) — Greenfield blueprint that drove the
  current architecture.
- [refactoring_v2.md](refactoring_v2.md) — Open follow-ups, anti-goals,
  roadmap.

## License

[Apache 2.0](LICENSE).
