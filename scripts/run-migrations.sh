#!/bin/sh

set -e

POSTGRES_HOST="${POSTGRES_HOST:-localhost}"
POSTGRES_PORT="${POSTGRES_PORT:-5432}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-postgres}"
POSTGRES_DATABASE="${POSTGRES_DATABASE:-quotes}"

MIGRATIONS_DIR="${MIGRATIONS_DIR:-/app/migrations}"
COMMAND="${COMMAND:-up}"

export PGPASSWORD="${POSTGRES_PASSWORD}"

echo "Starting database migrations..."
echo "Database: ${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DATABASE}"
echo "Command: ${COMMAND}"
echo "Migrations directory: ${MIGRATIONS_DIR}"

echo "Waiting for database to be ready..."
until psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -c '\q' 2>/dev/null; do
    echo "Database is unavailable - sleeping"
    sleep 1
done

echo "Database is ready!"

execute_migration() {
    local file="$1"
    echo "Executing migration: $(basename "$file")"
    if psql -h "${POSTGRES_HOST}" -p "${POSTGRES_PORT}" -U "${POSTGRES_USER}" -d "${POSTGRES_DATABASE}" -f "$file"; then
        echo "✓ Migration $(basename "$file") completed successfully"
        return 0
    else
        echo "✗ Migration $(basename "$file") failed"
        return 1
    fi
}

if [ "$COMMAND" = "up" ]; then
    for file in $(find "${MIGRATIONS_DIR}" -type f \( -name "*_up.sql" -o -name "*.sql" \) ! -name "*_down.sql" | sort -V); do
        case "$file" in
            *_down.sql) continue ;;
        esac
        execute_migration "$file"
    done
elif [ "$COMMAND" = "down" ]; then
    for file in $(find "${MIGRATIONS_DIR}" -type f -name "*_down.sql" | sort -Vr); do
        execute_migration "$file"
    done
else
    echo "Unknown command: ${COMMAND}. Use 'up' or 'down'"
    exit 1
fi

echo "All migrations completed successfully!"

