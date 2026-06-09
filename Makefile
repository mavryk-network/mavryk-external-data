.PHONY: build run test test-integration clean deps docker-build docker-run docker-stop \
        fmt lint docs openapi-lint openapi-render \
        migrate-up migrate-down migrate-reset migrate-redo

# --------------------------
# Config
# --------------------------
BINARY = bin/quotes

POSTGRES_HOST ?= localhost
POSTGRES_PORT ?= 5477
POSTGRES_USER ?= admin
POSTGRES_PASSWORD ?= qwerty
POSTGRES_DATABASE ?= quotes
export PGPASSWORD=$(POSTGRES_PASSWORD)

PSQL = psql -h $(POSTGRES_HOST) -p $(POSTGRES_PORT) -U $(POSTGRES_USER) -d $(POSTGRES_DATABASE)
MIGRATIONS_DIR ?= $(CURDIR)/migrations

# --------------------------
# Build & Run
# --------------------------
GIT_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_LDFLAGS := -w -s -X main.Version=$(GIT_SHA)

build:
	@echo "Building application (git=$(GIT_SHA))..."
	go build -trimpath -ldflags "$(BUILD_LDFLAGS)" -o $(BINARY) cmd/quotes/main.go

run:
	@echo "Running application..."
	go run cmd/quotes/main.go

jwt:
	@go run cmd/jwtgen/main.go $(JWT_ARGS)

test:
	@echo "Running tests..."
	go test -race -timeout 5m -cover ./...

# Integration tests run against a real TimescaleDB container (testcontainers).
# Docker must be available on the runner. Excluded from `make test` so the
# default loop stays fast and Docker-free.
test-integration:
	@echo "Running integration tests (Docker required)..."
	go test -race -timeout 10m -tags=integration ./tests/integration/...

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
# Database migrations (lexicographic order, idempotent .sql; same style as wallet-backend)
# --------------------------

# Apply all migrations from migrations/ in order.
migrate-up:
	@echo "Applying all migrations from $(MIGRATIONS_DIR) ..."
	@for f in $$(ls "$(MIGRATIONS_DIR)"/*.sql 2>/dev/null | sort); do \
		echo "  -> $$f"; \
		$(PSQL) -f "$$f" || exit 1; \
	done
	@echo "All migrations applied."

migrate-down:
	@echo "migrate-down: no rollback scripts defined."

migrate-reset: migrate-down migrate-up
	@echo "Database reset completed."

migrate-redo: migrate-up
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

# OpenAPI 3.0 spec lives in docs/openapi.yaml (canonical, hand-written).
# `redocly lint` (no args) reads .redocly.yaml at the repo root, which points
# to docs/openapi.yaml. Same command runs in CI (workflow: openapi-lint).
#
# Requires either Node (`npx`) or Docker — most dev machines have one.
openapi-lint:
	@if command -v npx >/dev/null 2>&1; then \
		npx --yes @redocly/cli@latest lint docs/openapi.yaml; \
	elif command -v docker >/dev/null 2>&1; then \
		docker run --rm -v $$PWD:/spec -w /spec redocly/cli \
			lint docs/openapi.yaml; \
	else \
		echo "Error: install Node.js (for npx) or Docker."; \
		exit 1; \
	fi

# Render a static HTML view (Redoc) for hosting / PR attachments.
openapi-render:
	@if command -v npx >/dev/null 2>&1; then \
		npx --yes @redocly/cli@latest build-docs docs/openapi.yaml -o docs/openapi.html; \
	elif command -v docker >/dev/null 2>&1; then \
		docker run --rm -v $$PWD:/spec -w /spec redocly/cli \
			build-docs docs/openapi.yaml -o docs/openapi.html; \
	else \
		echo "Error: install Node.js (for npx) or Docker."; \
		exit 1; \
	fi
