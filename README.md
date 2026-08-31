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
  Pair discovery is **automatic**: an hourly sync (plus one at startup)
  reconciles `rwa_pairs` and `rwa_launches` against Equiteez
  `in_allowlist=true` tokens/orderbooks — no manual seed required. Prices
  are normalized by the quote currency's decimals from the `tokens`
  registry (so micro-USDT `56_250_000` is stored as `56.25`). See
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
│   ├── openapi.yaml               # OpenAPI 3.0 spec (served at /openapi.yaml)
│   └── index.html                 # Swagger UI (served at /docs)
├── tests/integration/             # testcontainers TimescaleDB suite
└── README.md  ← you are here
```

## API

The authoritative API reference is the OpenAPI spec: **`/openapi.yaml`**,
browsable via Swagger UI at **`/docs`**. The table below is an index, not a
contract — request/response shapes live in the spec.

| Endpoint                                | Description                                          |
|-----------------------------------------|------------------------------------------------------|
| `GET /healthz`                          | Liveness — process up                                |
| `GET /readyz`                           | Readiness — DB ping + drain gate                     |
| `GET /metrics`                          | Prometheus exposition (internal listener; public only outside release mode in single-port setups) |
| `GET /quotes`                           | Legacy wide-format quotes (deprecated; superseded by `/v1/prices`) |
| `GET /v1/prices/:token`                 | FT price points (range or latest)                    |
| `GET /v1/prices/:token/latest`          | FT snapshot — all currencies, transposed             |
| `GET /v1/prices/:token/count`           | Total row count for token                            |
| `GET /v1/prices/:token/change`          | Price change over `?periods=` with bracketed anchors |
| `GET /v1/prices/:token/{series,ohlc}`   | Chart data from continuous aggregates (`?interval=`) |
| `GET /v1/rwa` 🔒                        | RWA market overview: every enabled asset with price, 24h change, mini-series |
| `GET /v1/rwa/:symbol` 🔒                | RWA price points; `:symbol` is `{base}-{quote}`, case-insensitive (e.g. `mars1-usdt`) |
| `GET /v1/rwa/:symbol/latest` 🔒         | RWA snapshot (`last` side) + optional `ath` / `price_one_year_ago` |
| `GET /v1/rwa/:symbol/change` 🔒         | RWA price change over `?periods=`                    |
| `GET /v1/rwa/:symbol/{series,ohlc}` 🔒  | RWA chart data (`?interval=`)                        |
| `GET /v1/pairs/rwa` 🔒                  | RWA pair/launch catalog (symbols, addresses, quote)  |
| `GET /v1/tickers/:token/latest`         | Per-exchange tickers: price, 24h volume, 1D change %, exchange logo; `?include_stale=true` opts into stale rows |
| `GET /v1/tickers/:token/distribution`   | Volume-distribution pie data; `?group_by=exchange\|target` |

🔒 — on the public listener these routes require an MBIO-issued RS256 Bearer
JWT (`Authorization: Bearer <token>`; 401/403 on failure). The intra-cluster
internal listener (`server.internal_port`) serves them without auth. Local
dev: `AUTH_ENABLED=false` or `make jwt` to mint a token.

### Common query parameters

| Param           | On endpoints                | Notes                                       |
|-----------------|-----------------------------|---------------------------------------------|
| `from` / `to`   | list, series, ohlc          | RFC3339; both omitted → "latest" mode (newest rows/buckets) |
| `limit`         | list, series, ohlc          | capped by `server.max_query_limit` (10000)  |
| `currency`      | FT list                     | filter: `usd`, `usd,eur`                    |
| `interval`      | series, ohlc                | `1m 5m 15m 1h 4h 1d`; window caps per interval (see spec) |
| `periods`       | change                      | subset of `1h,24h,7d,30d`                   |
| `in`            | RWA + tickers               | read-side conversion targets, e.g. `usd,eur,aed` (see [ADR-0013](docs/adr/0013-multi-currency-rwa-conversion-read-side.md)); capped by `server.max_in_currencies` (10) |
| `include_stale` | tickers /latest             | `true` returns rows older than `server.ticker_stale_after` (default 1h) flagged `is_stale: true` |
| `group_by`      | tickers /distribution       | `exchange` or `target` — required           |

RWA endpoints serve the `last` (trade) side only; there is no `side` filter.

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

# RWA snapshot in native quote (USDT), JWT-protected
curl -H "Authorization: Bearer $TOKEN" http://localhost:3010/v1/rwa/mars1-usdt/latest

# Same snapshot rendered into USD / EUR via FT-side FX — flat top-level keys
curl -H "Authorization: Bearer $TOKEN" "http://localhost:3010/v1/rwa/mars1-usdt/latest?in=usd,eur"
# →
# {
#   "timestamp": "2026-05-08T12:00:00Z",
#   "native_quote": "usdt",
#   "price": 100.45,
#   "market": "secondary",
#   "usd": 100.46,
#   "eur": 92.11
# }
# Failed conversions drop their key (still 200); a conversion served with a
# stale FX rate adds "fx": {"eur": {"rate_ts": "...", "stale": true}}.
```

### Response shapes

`GET /v1/prices/:token` — list of points (long-format):

```json
[
  {"timestamp": "2026-04-27T12:00:00Z", "currency": "usd", "price": 0.071541},
  {"timestamp": "2026-04-27T12:00:00Z", "currency": "eur", "price": 0.060941}
]
```

`GET /v1/prices/:token/latest` — transposed snapshot:

```json
{
  "source": "coingecko",
  "entity": "mvrk",
  "timestamp": "2026-04-27T12:00:00Z",
  "values": {"usd": 0.071541, "eur": 0.060941}
}
```

FT and RWA prices are JSON **numbers**: 6 decimal places at or above 0.01,
6 significant digits below it — so BTC/ETH-denominated quotes (≈7e-7) keep
their value instead of rounding to `0.000001` or `0`. Ticker prices are
full-precision JSON strings. (ADR-0003 originally specified string
serialisation everywhere — superseded for FT/RWA, see the note in
[ADR-0003](docs/adr/0003-decimal-for-numeric-precision.md).)

Errors use a stable envelope:

```json
{"code": "NOT_FOUND", "message": "Token not found"}
```

Codes: `INVALID_ARGUMENT` (400), `UNAUTHORIZED` (401), `FORBIDDEN` (403),
`NOT_FOUND` (404), `CONFLICT` (409), `RANGE_NOT_SATISFIABLE` (416),
`RATE_LIMITED` (429), `INTERNAL` (500), `NOT_IMPLEMENTED` /
`OHLCV_NOT_IMPLEMENTED` (501), `UNAVAILABLE` (503).

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
```

Token registry lives in the `tokens` table (loaded at startup;
[ADR-0010](docs/adr/0010-runtime-token-registry.md)). Per-token live/backfill
overrides go in `config.yaml` under `tokens:`; see [config.yaml](config.yaml).

## Running

### Prerequisites

- Go 1.25+
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
- Continuous aggregates (real-time): `token_prices_{1m,1h,1d}`,
  `rwa_quote_prices_{1m,1h,1d}`.
- Compression policies: 14d for FT, 7d for RWA, 7d for tickers. Retention:
  2 years on FT/RWA raw, 90d on tickers raw (no validated retro-analytics
  use yet — extend when a consumer asks).

## Observability

### Metrics (`/metrics`)

- `http_*` — request counts, latency histograms, response classes.
- `outbound_http_*` — retries, CB transitions, CB current state, RL wait.
- `job_tick_duration_seconds`, `job_errors_total`, `job_rows_affected_total`.
- `job_last_success_timestamp_seconds` (gauge), `job_tick_panics_total` — a job
  whose gauge stops advancing is failing or stalled even while the process looks
  healthy, so alert on `now() - job_last_success_timestamp_seconds` exceeding a
  few tick intervals. It reports whether the job is erroring, not whether data
  is arriving; pair it with `job_rows_affected_total` for the latter.
- `backfill_oldest_ts_seconds` (gauge), `backfill_auto_disabled_total`.
- `db_pool_*` — open / in-use / idle / wait-duration.

### Logs

Structured JSON via zerolog. Toggle level with `LOG_LEVEL=debug`. The
`logging_initialized` line is emitted once at startup with the active
level and caller-annotation flag (`LOG_CALLER=false` to disable).

Outbound HTTP logs carry `request_id` when triggered from an HTTP handler;
background-job logs carry `tick_id` for per-tick correlation.

### Profiling

`server.pprof_enabled: true` mounts `/debug/pprof/*` on the **internal
listener only** (`server.internal_port` must be set) — pprof endpoints
disclose stack traces and allow CPU-pinning profile requests.

## Testing

```bash
go test ./...                                        # fast, unit only
go test -race -timeout 10m -cover ./...              # what CI runs
go test -race -tags=integration ./tests/integration/ # real TimescaleDB (Docker)
```

- Unit: domain types, cache decorators, mappers, backfill helpers, HTTP
  handlers via `httptest`, JWT middleware battery, config validation.
- Integration (`tests/integration/`, `testcontainers-go`): one pinned
  TimescaleDB container per package running the real migrations —
  change anchors, chart candles + CAGG refreshes, FX at-or-before rates,
  pair/launch sync round-trips, backfill-state cursors.

## CI

`.github/workflows/main.yml`:

- `govulncheck` against the dependency tree.
- `golangci-lint`.
- `go test -race -coverprofile=coverage.out -covermode=atomic ./...`
  with coverage uploaded as an artifact.
- Integration tests (`-tags=integration`) on a real TimescaleDB container.
- OpenAPI lint (Redocly) over `docs/openapi.yaml`.
- `go build -trimpath -ldflags "-w -s"` of the binary.
- On tag pushes: multi-stage Docker image build + push + ArgoCD sync
  (gated by the `production` environment).

## Documentation

- [/docs](docs/index.html) (Swagger UI) + [docs/openapi.yaml](docs/openapi.yaml)
  — the API contract; always prefer these over prose.
- [docs/adr/](docs/adr/) — Architecture decision records covering storage
  shape, ORM choice, migration tool, generic-handler, caching, auth, charts.

## License

[Apache 2.0](LICENSE).
