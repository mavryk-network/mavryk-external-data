.PHONY: build run test clean deps docker-build docker-run docker-stop \
        migrate-up migrate-down migrate-reset migrate-redo fmt lint docs

# --------------------------
# Config
# --------------------------
BINARY = bin/quotes

POSTGRES_HOST ?= localhost
POSTGRES_PORT ?= 5432
POSTGRES_USER ?= postgres
POSTGRES_PASSWORD ?= postgres
POSTGRES_DATABASE ?= quotes
export PGPASSWORD=$(POSTGRES_PASSWORD)

MIGRATION_UP   = internal/core/infrastructure/storage/migrations/001_init.sql
MIGRATION_DOWN = internal/core/infrastructure/storage/migrations/001_init_down.sql

# --------------------------
# Build & Run
# --------------------------
build:
	@echo "Building application..."
	go build -o $(BINARY) cmd/quotes/main.go

run:
	@echo "Running application..."
	go run cmd/quotes/main.go

test:
	@echo "Running tests..."
	go test ./...

clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/

deps:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# --------------------------
# Docker
# --------------------------
docker-build:
	@echo "Building Docker image..."
	docker build -t quotes-service .

docker-run:
	@echo "Starting Docker Compose..."
	docker-compose -p quotes up -d

docker-stop:
	@echo "Stopping Docker Compose..."
	docker-compose -p quotes down

# --------------------------
# Database migrations
# --------------------------
migrate-up:
	@echo "Applying migration: $(MIGRATION_UP)"
	psql -h $(POSTGRES_HOST) -p $(POSTGRES_PORT) -U $(POSTGRES_USER) -d $(POSTGRES_DATABASE) -f $(MIGRATION_UP)

migrate-down:
	@echo "Rolling back migration: $(MIGRATION_DOWN)"
	psql -h $(POSTGRES_HOST) -p $(POSTGRES_PORT) -U $(POSTGRES_USER) -d $(POSTGRES_DATABASE) -f $(MIGRATION_DOWN)

migrate-reset: migrate-down migrate-up
	@echo "Database reset completed."

migrate-redo: migrate-up migrate-down migrate-up
	@echo "Migration redo completed."

# --------------------------
# Code quality
# --------------------------
fmt:
	@echo "Formatting code..."
	go fmt ./...

lint:
	@echo "Running linter..."
	golangci-lint run

# --------------------------
# Documentation
# --------------------------
docs:
	@echo "Starting godoc server at http://localhost:6060"
	godoc -http=:6060 &
