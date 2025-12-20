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

Interactive API documentation is available at:
- **Swagger UI**: `http://localhost:3010/swagger/index.html`
- **JSON spec**: `http://localhost:3010/swagger/doc.json`
- **YAML spec**: `http://localhost:3010/swagger/doc.yaml`

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
5. Saves new quotes to token-specific tables (e.g., `mev.mvrk`, `mev.usdt`).
6. API layer serves data using application and domain layers.
7. If a large time gap is detected, data is collected in chunks to avoid timeouts.


## Database schema

Each token has its own table in the `mev` schema:

```sql
-- Schema
CREATE SCHEMA IF NOT EXISTS mev;

-- MVRK token table (renamed from quotes)
CREATE TABLE mev.mvrk (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL,
    btc DECIMAL(20,8) DEFAULT 0,
    usd DECIMAL(20,8) DEFAULT 0,
    eur DECIMAL(20,8) DEFAULT 0,
    cny DECIMAL(20,8) DEFAULT 0,
    jpy DECIMAL(20,8) DEFAULT 0,
    krw DECIMAL(20,8) DEFAULT 0,
    eth DECIMAL(20,8) DEFAULT 0,
    gbp DECIMAL(20,8) DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- USDT token table
CREATE TABLE mev.usdt (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMPTZ NOT NULL,
    btc DECIMAL(20,8) DEFAULT 0,
    usd DECIMAL(20,8) DEFAULT 0,
    eur DECIMAL(20,8) DEFAULT 0,
    cny DECIMAL(20,8) DEFAULT 0,
    jpy DECIMAL(20,8) DEFAULT 0,
    krw DECIMAL(20,8) DEFAULT 0,
    eth DECIMAL(20,8) DEFAULT 0,
    gbp DECIMAL(20,8) DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- Indexes for each table
CREATE INDEX idx_mev_mvrk_timestamp ON mev.mvrk (timestamp);
CREATE INDEX idx_mev_usdt_timestamp ON mev.usdt (timestamp);
```

Tables can be converted to TimescaleDB hypertables for better time-series performance.


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

2. **Run migrations**:

Migrations are located in `internal/core/infrastructure/storage/migrations/` and are executed using native PostgreSQL client (`psql`).

**Using Docker Compose** (recommended):
```bash
docker-compose up migration
```

**Manually using the migration script**:
```bash
# Set database connection parameters
export POSTGRES_HOST=localhost
export POSTGRES_PORT=5432
export POSTGRES_USER=postgres
export POSTGRES_PASSWORD=postgres
export POSTGRES_DATABASE=quotes

# Run migrations
./scripts/run-migrations.sh
```

**Manually using psql**:
```bash
# Apply all up migrations in order
psql -h localhost -U postgres -d quotes -f internal/core/infrastructure/storage/migrations/001_init.sql
psql -h localhost -U postgres -d quotes -f internal/core/infrastructure/storage/migrations/002_add_usdt_table.up.sql
psql -h localhost -U postgres -d quotes -f internal/core/infrastructure/storage/migrations/003_rename_quotes_to_mvrk.up.sql
```

**Migration files structure**:
- `001_init.sql` - Creates schema, tables, and indexes
- `002_add_usdt_table.up.sql` - Creates USDT table
- `003_rename_quotes_to_mvrk.up.sql` - Renames quotes table to mvrk
- `*_down.sql` - Rollback migrations (for down migrations)

All migrations are **idempotent** and can be safely executed multiple times.

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
| `API_RATE_LIMIT_RPS`    | Internal per-second rate limit                 | 100                            |
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

The migration script (`scripts/run-migrations.sh`) provides:
- Automatic database health check before running migrations
- Support for `up` and `down` migration commands
- Idempotent migrations (safe to run multiple times)
- Configurable via environment variables

**Migration script environment variables**:
- `POSTGRES_HOST` - Database host (default: localhost)
- `POSTGRES_PORT` - Database port (default: 5432)
- `POSTGRES_USER` - Database user (default: postgres)
- `POSTGRES_PASSWORD` - Database password (default: postgres)
- `POSTGRES_DATABASE` - Database name (default: quotes)
- `MIGRATIONS_DIR` - Path to migrations directory (default: /app/migrations)
- `COMMAND` - Migration command: `up` or `down` (default: up)
