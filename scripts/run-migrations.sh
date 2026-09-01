#!/bin/sh

# Runs forward migrations in lexicographic order (migrations/001_*.sql, 002_*.sql, ...).
# Same contract as `make migrate-up` / mavryk-wallet-backend: idempotent .sql, no down files.

set -e

POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
POSTGRES_DATABASE="${POSTGRES_DATABASE:-quotes}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/app/migrations}"
COMMAND="${COMMAND:-up}"

export PGPASSWORD="${POSTGRES_PASSWORD}"

if [ "$COMMAND" = "down" ]; then
  echo "migrate-down: no rollback scripts defined (same as mavryk-wallet-backend)."
  exit 0
fi

if [ ! -d "$MIGRATIONS_DIR" ]; then
  echo "Migrations directory not found: $MIGRATIONS_DIR" >&2
  exit 1
fi

echo "Starting database migrations..."
echo "Database: ${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DATABASE}"
echo "Migrations directory: ${MIGRATIONS_DIR}"

echo "Waiting for database to be ready..."
attempts=0
max_attempts="${DB_WAIT_MAX_ATTEMPTS:-60}"
until psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -c '\q' 2>/dev/null; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge "$max_attempts" ]; then
    # Bounded wait: a wrong host/credentials fails identically every second, so
    # without a cap the whole stack hangs forever (app depends_on this service).
    # Surface the real psql error (auth vs connection refused) on the way out.
    echo "Database still unavailable after ${max_attempts} attempts; last psql error:" >&2
    psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -c '\q' >&2 || true
    exit 1
  fi
  echo "Database is unavailable - sleeping (${attempts}/${max_attempts})"
  sleep 1
done

echo "Database is ready!"

if ! ls "$MIGRATIONS_DIR"/*.sql >/dev/null 2>&1; then
  echo "No *.sql files in ${MIGRATIONS_DIR}" >&2
  exit 1
fi

# All files run in ONE psql session behind pg_advisory_lock, so concurrent
# runners (rolling deploy, retried job) serialize instead of racing on CREATE;
# the session lock releases automatically on exit, success or failure.
#
# -v ON_ERROR_STOP=1: without it psql runs past a failed statement and still
# exits 0, so a half-applied migration would be reported as success. We do NOT
# add --single-transaction: the CAGG/policy files (0006-0010) call
# add_continuous_aggregate_policy / add_*_policy, which cannot run inside a
# transaction block. ON_ERROR_STOP still aborts the whole run on any error.
MIGRATION_LOCK_KEY="${MIGRATION_LOCK_KEY:-792015843}"
# The key is interpolated into SQL below — refuse anything but a plain integer.
if ! printf '%s' "$MIGRATION_LOCK_KEY" | grep -Eq '^-?[0-9]{1,18}$'; then
  echo "✗ MIGRATION_LOCK_KEY must be an integer, got: ${MIGRATION_LOCK_KEY}" >&2
  exit 1
fi
RUNNER_SQL="$(mktemp)"
trap 'rm -f "$RUNNER_SQL"' EXIT

{
  # Bound the lock wait: an orphaned session would otherwise block every
  # future deploy silently. On timeout ON_ERROR_STOP fails the run loudly
  # (find the holder via pg_locks locktype='advisory').
  echo "SET statement_timeout = '10min';"
  echo "SELECT pg_advisory_lock(${MIGRATION_LOCK_KEY});"
  # Cleared once the lock is held — index builds can legitimately run long.
  echo "SET statement_timeout = 0;"
  for f in $(ls "$MIGRATIONS_DIR"/*.sql | sort); do
    echo "\\echo Executing migration: $(basename "$f")"
    echo "\\i $f"
  done
} > "$RUNNER_SQL"

if psql -v ON_ERROR_STOP=1 -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -f "$RUNNER_SQL"; then
  echo "All migrations completed successfully!"
else
  echo "✗ Migration run failed" >&2
  exit 1
fi
