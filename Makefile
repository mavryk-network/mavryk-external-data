.PHONY: build run test clean deps docker-build docker-run docker-stop \
        migrate-up migrate-down migrate-reset migrate-redo fmt lint docs swagger

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
MIGRATIONS_DIR = internal/core/infrastructure/storage/migrations
MIGRATE_BINARY = bin/migrate

migrate-build:
	@echo "Building migrate tool..."
	@go build -o $(MIGRATE_BINARY) cmd/migrate/main.go

migrate-up: migrate-build
	@echo "Applying all pending migrations..."
	@$(MIGRATE_BINARY) -command=up

migrate-down: migrate-build
	@echo "Rolling back migrations..."
	@if [ -z "$(STEPS)" ]; then \
		echo "Error: STEPS parameter is required for safety"; \
		echo "Example: make migrate-down STEPS=1"; \
		exit 1; \
	fi
	@$(MIGRATE_BINARY) -command=down -steps=$(STEPS)

migrate-status: migrate-build
	@echo "Checking migration status..."
	@$(MIGRATE_BINARY) -command=version

migrate-force: migrate-build
	@echo "Forcing migration version..."
	@if [ -z "$(VERSION)" ]; then \
		echo "Error: VERSION parameter is required"; \
		echo "Example: make migrate-force VERSION=3"; \
		exit 1; \
	fi
	@$(MIGRATE_BINARY) -command=force -version=$(VERSION)

migrate-create: migrate-build
	@if [ -z "$(NAME)" ]; then \
		echo "Error: NAME parameter is required"; \
		echo "Example: make migrate-create NAME=add_new_feature"; \
		exit 1; \
	fi
	@$(MIGRATE_BINARY) -command=create $(NAME)
	@echo "Migration created successfully"

migrate-list:
	@echo "Available migrations:"
	@ls -1 $(MIGRATIONS_DIR)/*.up.sql 2>/dev/null | sort | sed 's/.*\///' | nl -w2 -s'. ' || echo "No migrations found"

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

swagger:
	@echo "Generating Swagger documentation..."
	@if command -v swag >/dev/null 2>&1; then \
		swag init -g cmd/quotes/main.go -o ./docs; \
	elif [ -f ~/go/bin/swag ]; then \
		~/go/bin/swag init -g cmd/quotes/main.go -o ./docs; \
	else \
		echo "Error: swag not found. Install it with: go install github.com/swaggo/swag/cmd/swag@latest"; \
		exit 1; \
	fi
