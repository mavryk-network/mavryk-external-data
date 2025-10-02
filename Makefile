.PHONY: build run test clean deps docker-build docker-run

# Build the application
build:
	go build -o bin/quotes cmd/quotes/main.go

# Run the application
run:
	go run cmd/quotes/main.go

# Run tests
test:
	go test ./...

# Clean build artifacts
clean:
	rm -rf bin/

# Install dependencies
deps:
	go mod download
	go mod tidy

# Build Docker image
docker-build:
	docker build -t quotes-service .

# Run with Docker Compose
docker-run:
	docker-compose up -d

# Stop Docker Compose
docker-stop:
	docker-compose down

# Database migration
migrate-up:
	psql -h localhost -U admin -d mvkt_quotes -f internal/core/infrastructure/storage/migrations/001_init.sql

# Check code formatting
fmt:
	go fmt ./...

# Run linter
lint:
	golangci-lint run

# Generate documentation
docs:
	godoc -http=:6060
