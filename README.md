# Mavryk External Data

**Mavryk External Data** is a high-performance Go service for collecting and serving Mavryk and other currency quotes, built with clean architecture (hexagonal architecture).


## Key features

* **Automated data collection**: Fetches quotes from the CoinGecko API.
* **Multiple currencies**: BTC, USD, EUR, CNY, JPY, KRW, ETH, GBP.
* **Restful API**: Provides endpoints to query quotes.
* **Background jobs**: Hosted job for periodic data updates.
* **Efficient storage**: PostgreSQL with indexes for fast queries.
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
│           ├── storage/          # Database layer (entities, repositories, migrations)
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
* **Database**: PostgreSQL with GIN indexes
* **Configuration**: YAML + environment variables
* **Background processing**: Hosted job via goroutines and timers


## API endpoints

| Endpoint                   | Description                     | Parameters            |
| -------------------------- | ------------------------------- | --------------------- |
| `GET /health`              | Service health check            | —                     |
| `GET /quotes`       | Retrieve quotes                 | `from`, `to`, `limit` |
| `GET /quotes/last`  | Retrieve the latest quote       | —                     |
| `GET /quotes/count` | Retrieve total number of quotes | —                     |

## API Examples

### Get quotes with filters
```bash
# Get quotes from last 24 hours
curl "http://localhost:8080/quotes?from=2023-10-01T00:00:00Z&to=2023-10-02T00:00:00Z"

# Get last 50 quotes
curl "http://localhost:8080/quotes?limit=50"

# Get quotes with pagination (if limit is reached, use last timestamp + 1s for next request)
curl "http://localhost:8080/quotes?from=2023-10-01T00:00:00Z&to=2023-10-02T00:00:00Z&limit=100"
```

### Get latest quote
```bash
curl "http://localhost:8080/quotes/last"
```

### Get quotes count
```bash
curl "http://localhost:8080/quotes/count"
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

1. A background job runs every N seconds (configurable).
2. Fetches data from CoinGecko API:

   ```
   coins/mavryk-network/market_chart/range?vs_currency={cur}&from={unix}&to={unix}
   ```
3. Sample JSON response:

   ```json
   {
     "prices": [[timestamp_ms, price], ...],
     "market_caps": [[timestamp_ms, value], ...],
     "total_volumes": [[timestamp_ms, value], ...]
   }
   ```
4. Normalizes timestamps to seconds, applies forward-fill for missing values.
5. Saves new quotes to PostgreSQL and updates in-memory cache.
6. API layer serves data using application and domain layers.


## Database schema

```sql
CREATE TABLE quotes (
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

CREATE INDEX idx_quotes_timestamp ON quotes (timestamp);
```


## Quick start

### Prerequisites

* Go 1.21+
* PostgreSQL 12+

### Installation

```bash
git clone <repository-url>
cd quotes
go mod tidy
```

### Database setup

```sql
CREATE SCHEMA mev;
CREATE DATABASE mavryk_external_data;
```

### Configuration

1. **YAML** (`config.yaml`)
2. **Environment variables** (`.env`)
3. **Command line overrides**

> Environment variables override YAML configuration.

#### Environment variables

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
| `JOB_INTERVAL_SECONDS`  | Quotes collector interval (seconds)            | 60                             |
| `JOB_ENABLED`           | Enable quotes collector job (true/false)       | false                          |
| `API_TIMEOUT_SECONDS`   | HTTP client timeout (seconds)                  | 30                             |
| `API_RATE_LIMIT_RPS`    | Internal per-second rate limit                 | 100                            |
| `COINGECKO_API_KEY`     | CoinGecko API key (if required)                | —                              |
| `COINGECKO_BASE_URL`    | CoinGecko API base URL                         | `https://api.coingecko.com/api/v3` |
| `BACKFILL_ENABLED`      | Enable historical backfill on startup          | false                          |
| `BACKFILL_START_FROM`   | Backfill start timestamp (RFC3339 or `YYYY-MM-DD`) | —                           |
| `BACKFILL_SLEEP_MS`     | Delay between backfill chunks (ms)             | 3000                           |
| `BACKFILL_CHUNK_MINUTES`| Size of each backfill window (minutes)         | 5                              |

### Run

```bash
go run cmd/quotes/main.go
```

The service starts at `http://localhost:3010` and begins collecting quotes every minute (interval configurable).


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

The service includes a hosted job that:

1. Runs every N seconds (configurable)
2. Fetches data from CoinGecko API
3. Normalizes timestamps to seconds
4. Applies forward-fill for missing data
5. Saves new quotes to the database and updates cache

### Backfill (historical data)

Backfill lets you pre-populate the database with historical quotes from CoinGecko. When enabled, it runs once at startup before the periodic collector begins.

- Backfill is controlled via configuration (env vars/YAML)
- If `BACKFILL_START_FROM` is empty, backfill is skipped
- The process resumes from the last stored timestamp if it is later than `START_FROM`
- Data is fetched in time windows (chunks) with a sleep between chunks to respect provider limits

Configuration:

| Setting | Description |
| ------- | ----------- |
| `BACKFILL_ENABLED` | Set to `true` to run backfill on startup |
| `BACKFILL_START_FROM` | RFC3339 or `YYYY-MM-DD` start time, e.g. `2025-09-18` or `2025-09-18T00:00:00Z` |
| `BACKFILL_CHUNK_MINUTES` | Window size for each request (minutes). Larger windows reduce API calls but may return sparse points |
| `BACKFILL_SLEEP_MS` | Delay between chunks (ms). Increase to be gentle with rate limits |

Examples

Run locally with environment variables:

```bash
export BACKFILL_ENABLED=true
export BACKFILL_START_FROM="2025-09-18"
export BACKFILL_CHUNK_MINUTES=360   # 6 hours per chunk
export BACKFILL_SLEEP_MS=3000       # 3s between chunks
go run cmd/quotes/main.go
```

Using docker-compose (already wired):

```yaml
services:
  quotes:
    environment:
      BACKFILL_ENABLED: "true"
      BACKFILL_START_FROM: "2025-09-18"
      BACKFILL_CHUNK_MINUTES: 360
      BACKFILL_SLEEP_MS: 3000
```

Notes

- Backfill runs only at startup. After completion, the periodic job continues with live collection.
- If the database is already up-to-date (within ~60s of now), backfill is skipped.
- Accepted `START_FROM` formats: `YYYY-MM-DD` or full RFC3339.
- Choose chunk and sleep values mindful of provider limits; defaults are conservative.
