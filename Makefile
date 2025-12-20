.PHONY: build run test clean deps docker-build docker-run docker-stop \
        fmt lint docs swagger

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
