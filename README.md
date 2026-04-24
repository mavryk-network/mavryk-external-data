# Mavryk External Data

**Mavryk External Data** is a high-performance Go service for collecting and serving cryptocurrency quotes (MVRK, USDT, and more), built with clean architecture (hexagonal architecture).


## Key features

* **Multi-token support**: Collects and serves data for multiple tokens (MVRK, USDT, etc.)
* **Automated data collection**: Fetches quotes from the CoinGecko API for each token.
* **Multiple currencies**: BTC, USD, EUR, CNY, JPY, KRW, ETH, GBP.
* **Token-specific configuration**: Individual settings for each token (intervals, timeouts, backfill).
* **Restful API**: Provides endpoints to query quotes by token.
* **Background jobs**: Hosted jobs for periodic data updates per token.
* **Efficient storage**: PostgreSQL with TimescaleDB support and indexes for fast queries.
* **Clean architecture**: Well-structured, testable, and maintainable codebase.


## Architecture

```
mavryk-external-data/
├── cmd/quotes/                    # Application entry point
├── internal/
│   ├── config/                   # Configuration management
│   └── core/
│       ├── api/http/             # HTTP layer (handlers, router)
│       ├── application/quotes/   # Use cases (actions)
│       ├── domain/quotes/        # Domain models
│       └── infrastructure/       # External dependencies
│           ├── interactions/     # External APIs (CoinGecko)
│           ├── storage/          # Database layer (entities, repositories)
│           └── jobs/             # Background jobs (hosted jobs)
└── config.yaml                   # Configuration file
```

### Key principles

* **Clean architecture / hexagonal architecture**
* **Dependency inversion**: Application layer depends only on interfaces.
* **Event-driven**: supports future integration via message brokers.
* **In-memory caching**: caching for fast access.

```
API  → Application ← Infrastructure
      ↑
      Domain
```


## Technology stack

* **Language**: Go
* **Framework**: Gin (HTTP)
* **ORM**: GORM
* **Database**: PostgreSQL with TimescaleDB support
* **Migrations**: Native PostgreSQL (`psql`) - no external migration tools required
* **Configuration**: YAML + environment variables
* **Background processing**: Hosted jobs via goroutines and timers
* **API Documentation**: Swagger/OpenAPI
* **Containerization**: Docker with multi-stage builds


## API endpoints

| Endpoint                   | Description                              | Parameters            |
| -------------------------- | ---------------------------------------- | --------------------- |
| `GET /health`              | Service health check                     | —                     |
| `GET /quotes`              | Retrieve quotes for MVRK (legacy)       | `from`, `to`, `limit` |
| `GET /quotes/last`         | Retrieve the latest MVRK quote (legacy)  | —                     |
| `GET /quotes/count`        | Retrieve total number of MVRK quotes     | —                     |
| `GET /:token`              | Retrieve quotes for specific token       | `from`, `to`, `limit` |
| `GET /swagger/*any`        | Swagger API documentation                | —                     |

**Supported tokens**: `mvrk`, `usdt`

### API Documentation (Swagger)

**Swagger UI** loads the spec from **`/swagger.json`**, which is generated per request: `host` and `schemes` match the incoming request (and `X-Forwarded-Proto` behind ingress), same pattern as **mavryk-wallet-backend** — **Try it out** hits the same host you used to open `/swagger`.

This API exposes **`GET /:token`** at the site root (e.g. `/usdt`). Swagger is wired the same way as **mavryk-wallet-backend**: dynamic **`/swagger.json`**, then **`GET /swagger/*any`** for the UI — register those before **`GET /:token`** so `swagger` is not treated as a token. Open the UI at **`/swagger/index.html`** (same as wallet-backend). If you still see **Failed to fetch**, confirm the app is running and try a normal browser (some IDE previews block `localhost`).

Interactive API documentation is available at:
- **Swagger UI**: `http://localhost:3010/swagger/index.html`
- **OpenAPI JSON** (runtime, correct host for Try it out): `http://localhost:3010/swagger.json`
- **YAML** (committed artifact, `make swagger`): `docs/swagger.yaml`

To regenerate Swagger documentation after adding or modifying endpoints:
```bash
make swagger
```

## API Examples

### Get quotes by token
```bash
# Get MVRK quotes from last 24 hours
curl "http://localhost:3010/mvrk?from=2025-10-01T00:00:00Z&to=2025-10-02T00:00:00Z"

# Get USDT quotes with limit
curl "http://localhost:3010/usdt?limit=50"

# Get quotes with pagination (if limit is reached, use last timestamp + 1s for next request)
curl "http://localhost:3010/mvrk?from=2025-10-01T00:00:00Z&to=2025-10-02T00:00:00Z&limit=100"
```

### Legacy endpoints (MVRK only)
```bash
# Get MVRK quotes (legacy endpoint)
curl "http://localhost:3010/quotes?from=2025-10-01T00:00:00Z&to=2025-10-02T00:00:00Z"

# Get latest MVRK quote (legacy endpoint)
curl "http://localhost:3010/quotes/last"

# Get MVRK quotes count (legacy endpoint)
curl "http://localhost:3010/quotes/count"
```

### Response Format

**Get quotes** (`GET /quotes`):
```json
[
  {
    "timestamp": "2025-10-02T09:23:09Z",
    "btc": 6e-7,
    "usd": 0.0715412,
    "eur": 0.06094094,
    "cny": 0.50934472,
    "jpy": 10.5254412,
    "krw": 100.1782711,
    "eth": 0.00001633,
    "gbp": 0.05307935
  }
]
```

**Get latest quote** (`GET /quotes/last`):
```json
{
  "timestamp": "2025-10-02T09:23:09Z",
  "btc": 6e-7,
  "usd": 0.0715412,
  "eur": 0.06094094,
  "cny": 0.50934472,
  "jpy": 10.5254412,
  "krw": 100.1782711,
  "eth": 0.00001633,
  "gbp": 0.05307935
}
```

**Get count** (`GET /quotes/count`):
```json
{
  "count": 1500
}
```

### Pagination Strategy

When requesting quotes with a limit:
- If the response contains exactly `limit` records, make another request with `from = last_timestamp + 1s`
- Continue until you get fewer than `limit` records
- All timestamps are in UTC format (`yyyy-MM-ddTHH:mm:ssZ`)

## Data flow

1. Background jobs run independently for each token with configurable intervals.
2. For each token, fetches data from CoinGecko API:

   ```
   coins/{coin-id}/market_chart/range?vs_currency={cur}&from={unix}&to={unix}
   ```
   
   Coin IDs:
   - MVRK: `mavryk-network`
   - USDT: `tether`

3. Sample JSON response:

   ```json
   {
     "prices": [[timestamp_ms, price], ...],
     "market_caps": [[timestamp_ms, value], ...],
     "total_volumes": [[timestamp_ms, value], ...]
   }
   ```
4. Normalizes timestamps to seconds, applies forward-fill for missing values.
5. Saves new quotes to the unified `quotes` hypertable with the token identifier as a column.
6. API layer serves data using application and domain layers.
7. If a large time gap is detected, data is collected in chunks to avoid timeouts.


## Database schema

All quotes live in a single TimescaleDB hypertable `quotes`, partitioned by `timestamp`.
The `token` column identifies the instrument; adding a new supported token does **not** require a DDL change — just a whitelist entry in the domain layer.

```sql
CREATE TABLE quotes (
    token      text          NOT NULL,
    timestamp  timestamptz   NOT NULL,
    btc        numeric(20,8) DEFAULT 0,
    usd        numeric(20,8) DEFAULT 0,
    eur        numeric(20,8) DEFAULT 0,
    cny        numeric(20,8) DEFAULT 0,
    jpy        numeric(20,8) DEFAULT 0,
    krw        numeric(20,8) DEFAULT 0,
    eth        numeric(20,8) DEFAULT 0,
    gbp        numeric(20,8) DEFAULT 0,
    created_at timestamptz   NOT NULL DEFAULT NOW(),
    PRIMARY KEY (token, timestamp)
);

SELECT create_hypertable('quotes', 'timestamp', chunk_time_interval => INTERVAL '7 days');

CREATE INDEX idx_quotes_token_timestamp_desc ON quotes (token, timestamp DESC);
```

Inserts use `ON CONFLICT (token, timestamp) DO NOTHING`, so collectors and the backfill job can run concurrently without producing duplicates.


## Quick start

### Prerequisites

* Go 1.21+
* PostgreSQL 12+ (or Docker with docker-compose)
* PostgreSQL client (`psql`) for running migrations manually (optional if using Docker)

### Installation

```bash
git clone <repository-url>
cd quotes
go mod tidy
```

### Database setup

1. **Create database** (if not using Docker):

```sql
CREATE DATABASE mavryk_external_data;
```

2. **Run migrations** (forward-only `.sql` in `migrations/`, same idea as `mavryk-wallet-backend/migrations`); they apply in **lexicographic** order, are idempotent, and are safe to re-run:

**From the Makefile** (local `psql`):
```bash
make migrate-up
```

`migrate-down` prints a notice (no rollback files). `migrate-reset` and `migrate-redo` follow the same pattern as wallet-backend: **redo** and **reset** are essentially **migrate-up** again (down is a no-op).

**Using Docker Compose**:
```bash
docker-compose up migration
```

**Using the shell script** (e.g. local DB; optional `MIGRATIONS_DIR=./migrations`):
```bash
export POSTGRES_HOST=localhost
export POSTGRES_PORT=5432
export POSTGRES_USER=postgres
export POSTGRES_PASSWORD=postgres
export POSTGRES_DATABASE=quotes
./scripts/run-migrations.sh
```

**One file by hand**:
```bash
psql -h localhost -U postgres -d quotes -f migrations/001_init.sql
```

**`migrations/001_init.sql`**: creates the `quotes` table, enables TimescaleDB (if available), creates the hypertable, index `(token, timestamp DESC)`.

All forward migrations are **idempotent** and can be run multiple times. Rollback scripts are not maintained (as in wallet-backend); drop objects manually in dev if needed.

### Configuration

1. **YAML** (`config.yaml`)
2. **Environment variables** (`.env`)
3. **Command line overrides**

> Environment variables override YAML configuration.

#### Environment variables

**Global settings:**

| Name                    | Description                                   | Default                        |
| ----------------------- | --------------------------------------------- | ------------------------------ |
| `SERVER_HOST`           | Server bind address                            | 0.0.0.0                        |
| `SERVER_PORT`           | Server port                                    | 3010                           |
| `SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS` | Latest-quote in-process cache TTL (seconds); `0` disables | 5 (from config default if unset) |
| `SERVER_CORS_ALLOWED_ORIGINS` | Comma-separated allowed browser `Origin` values (http/https only; no `*`) | from `config.yaml` / built-in dev list |
| `POSTGRES_HOST`         | Postgres host                                  | localhost                      |
| `POSTGRES_PORT`         | Postgres port                                  | 5432                           |
| `POSTGRES_USER`         | Postgres user                                  | postgres                       |
| `POSTGRES_PASSWORD`     | Postgres password                              | postgres                       |
| `POSTGRES_DATABASE`     | Postgres database name                         | quotes                         |
| `POSTGRES_SSL`          | Postgres SSL mode                              | disable                        |
| `POSTGRES_LOGGING`      | Enable GORM SQL logging (true/false)           | false                          |
| `JOB_INTERVAL_SECONDS`  | Default quotes collector interval (seconds)     | 60                             |
| `JOB_ENABLED`           | Enable quotes collector job (true/false)       | false                          |
| `API_TIMEOUT_SECONDS`   | Default HTTP client timeout (seconds)          | 30                             |
| `API_RATE_LIMIT_RPS`    | CoinGecko outbound token-bucket (req/s)        | 10                             |
| `COINGECKO_API_KEY`     | CoinGecko API key (if required)                | —                              |
| `COINGECKO_BASE_URL`    | CoinGecko API base URL                         | `https://api.coingecko.com/api/v3` |
| `BACKFILL_ENABLED`      | Default: enable historical backfill            | false                          |
| `BACKFILL_START_FROM`   | Default backfill start (RFC3339 or `YYYY-MM-DD`) | —                           |
| `BACKFILL_SLEEP_MS`     | Default delay between backfill chunks (ms)     | 3000                           |
| `BACKFILL_CHUNK_MINUTES`| Default size of backfill window (minutes)      | 5                              |

**Token-specific settings** are configured in `config.yaml` under the `tokens` section. See [Token Configuration](#token-configuration) below.

### Run

**Local development**:
```bash
go run cmd/quotes/main.go
```

**Using Docker Compose**:
```bash
# Start all services (postgres, migrations, app)
docker-compose up -d

# View logs
docker-compose logs -f app

# Stop services
docker-compose down
```

The service starts at `http://localhost:3010` and begins collecting quotes for each enabled token according to their individual intervals (configurable per token).


## Example usage

```bash
# Get the latest quote
curl http://localhost:3010/quotes/last

# Get quotes from the last 24 hours
curl "http://localhost:3010/quotes?from=2025-09-30T00:00:00Z&to=2025-10-01T00:00:00Z"

# Get total quote count
curl http://localhost:3010/quotes/count
```


## Development

### Project layers

* **Domain layer**: Core business logic and entities (`Quote`)
* **Application layer**: Use cases / actions (`get_latest`, `get_count`, `get_all`)
* **Infrastructure layer**: Database, external APIs (CoinGecko)
* **API layer**: HTTP handlers and routing (Gin)

### Background jobs

The service includes hosted jobs for each token that:

1. Run independently with token-specific intervals (configurable per token)
2. Fetch data from CoinGecko API using token-specific CoinGecko coin IDs
3. Normalize timestamps to seconds
4. Apply forward-fill for missing data
5. Save new quotes to token-specific database tables
6. Automatically handle large time gaps by collecting data in chunks

**Features:**
- Each token has its own collection goroutine with individual ticker
- Token-specific timeouts and intervals
- Automatic catch-up: if a large time gap is detected, data is collected in configurable chunks
- Parallel backfill: each token can run backfill independently

### Token Configuration

Each token can have individual settings in `config.yaml`:

```yaml
tokens:
  mvrk:
    interval_seconds: 60        # Collection interval (0 = use global)
    enabled: true               # Enable/disable collection
    timeout_seconds: 30         # HTTP timeout (0 = use global)
    min_time_range_seconds: 60  # Minimum time range to collect
    max_chunk_minutes: 60      # Max chunk size for catch-up
    backfill:
      enabled: false            # Enable backfill for this token
      start_from: ""            # Backfill start date (overrides global)
      sleep_ms: 0               # Delay between chunks (0 = use global)
      chunk_minutes: 0          # Chunk size (0 = use global)
  usdt:
    interval_seconds: 120
    enabled: true
    timeout_seconds: 45
    min_time_range_seconds: 60
    max_chunk_minutes: 60
    backfill:
      enabled: true
      start_from: "2025-01-01"
      sleep_ms: 2000
      chunk_minutes: 10
```

**Settings explanation:**
- `interval_seconds`: How often to collect data for this token
- `enabled`: Enable/disable collection for this token
- `timeout_seconds`: HTTP timeout for API requests
- `min_time_range_seconds`: Minimum time difference to trigger collection
- `max_chunk_minutes`: Maximum chunk size when catching up on large time gaps
- `backfill.enabled`: Enable token-specific backfill
- `backfill.start_from`: Token-specific backfill start date
- `backfill.sleep_ms`: Delay between backfill chunks for this token
- `backfill.chunk_minutes`: Backfill chunk size for this token

**Value `0` means**: Use global setting from `job.*` or `backfill.*` sections.

### Backfill (historical data)

Backfill lets you pre-populate the database with historical quotes from CoinGecko. It can be configured globally or per-token.

**Global backfill** (applies to all tokens unless overridden):
- Controlled via `backfill.*` in `config.yaml` or environment variables
- If `BACKFILL_START_FROM` is empty, backfill is skipped
- The process resumes from the last stored timestamp if it is later than `START_FROM`
- Data is fetched in time windows (chunks) with a sleep between chunks

**Token-specific backfill**:
- Configured in `tokens.{token}.backfill.*` in `config.yaml`
- Overrides global settings when specified
- Each token can have its own backfill schedule and settings

**Configuration:**

| Setting | Description |
| ------- | ----------- |
| `BACKFILL_ENABLED` | Set to `true` to run backfill on startup (global) |
| `BACKFILL_START_FROM` | RFC3339 or `YYYY-MM-DD` start time, e.g. `2025-09-18` or `2025-09-18T00:00:00Z` |
| `BACKFILL_CHUNK_MINUTES` | Window size for each request (minutes). Larger windows reduce API calls but may return sparse points |
| `BACKFILL_SLEEP_MS` | Delay between chunks (ms). Increase to be gentle with rate limits |

**Examples:**

Run locally with environment variables (global backfill):

```bash
export BACKFILL_ENABLED=true
export BACKFILL_START_FROM="2025-09-18"
export BACKFILL_CHUNK_MINUTES=360   # 6 hours per chunk
export BACKFILL_SLEEP_MS=3000       # 3s between chunks
go run cmd/quotes/main.go
```

Using `config.yaml` (token-specific backfill):

```yaml
tokens:
  usdt:
    backfill:
      enabled: true
      start_from: "2025-01-01"
      chunk_minutes: 10
      sleep_ms: 2000
```

**Notes:**
- Backfill runs only at startup. After completion, the periodic job continues with live collection.
- If the database is already up-to-date (within ~60s of now), backfill is skipped.
- Accepted `START_FROM` formats: `YYYY-MM-DD` or full RFC3339.
- Choose chunk and sleep values mindful of provider limits; defaults are conservative.
- Each token runs backfill in parallel if enabled.

## Docker

### Building and running with Docker

The project includes a multi-stage Dockerfile and docker-compose configuration:

**Build images**:
```bash
docker-compose build
```

**Run all services**:
```bash
# Start postgres, run migrations, and start the app
docker-compose up -d

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

The `postgres` service uses the official **TimescaleDB** image (`timescale/timescaledb`, default `latest-pg16`) so the `timescaledb` extension and hypertables work locally. Optional: set `TIMESCALEDB_IMAGE` in `.env` to pin a tag. If you previously used plain `postgres` and see init errors, reset the named volume: `docker compose down -v` (destroys local DB data).

**Run migrations only**:
```bash
docker-compose up migration
```

**Docker stages**:
- `builder` - Builds the Go application
- `migration` - Runs database migrations using native `psql`
- `production` - Final lightweight image with the compiled application

**Environment variables** for Docker are configured in `docker-compose.yml` or can be set via `.env` file.

### Migration script

The migration script (`scripts/run-migrations.sh`) applies all `migrations/*.sql` in order (same contract as `make migrate-up`):
- Waits until Postgres accepts connections
- `COMMAND=down` is a no-op (no rollback files, as in wallet-backend)
- Configurable via environment variables

**Migration script environment variables**:
- `POSTGRES_HOST` - Database host (default: localhost)
- `POSTGRES_PORT` - Database port (default: 5432)
- `POSTGRES_USER` - Database user (default: postgres)
- `POSTGRES_PASSWORD` - Database password (default: postgres)
- `POSTGRES_DATABASE` - Database name (default: quotes)
- `MIGRATIONS_DIR` - Path to migrations directory (default: `/app/migrations` in Docker; for local use point at the repo’s `migrations/`)
- `COMMAND` - `up` (default) runs migrations; `down` prints a notice and exits
