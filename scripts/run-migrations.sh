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
until psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -c '\q' 2>/dev/null; do
  echo "Database is unavailable - sleeping"
  sleep 1
done

echo "Database is ready!"

if ! ls "$MIGRATIONS_DIR"/*.sql >/dev/null 2>&1; then
  echo "No *.sql files in ${MIGRATIONS_DIR}" >&2
  exit 1
fi

for f in $(ls "$MIGRATIONS_DIR"/*.sql 2>/dev/null | sort); do
  echo "Executing migration: $(basename "$f")"
  if psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -f "$f"; then
    echo "✓ Migration $(basename "$f") completed successfully"
  else
    echo "✗ Migration $(basename "$f") failed" >&2
    exit 1
  fi
done

echo "All migrations completed successfully!"
