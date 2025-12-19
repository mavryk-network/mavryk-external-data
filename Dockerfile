# =========================
# Builder stage
# =========================
FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main cmd/quotes/main.go

# =========================
# Migration stage
# =========================
FROM golang:1.24-alpine AS migration

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build migrate tool
RUN go build -o migrate cmd/migrate/main.go

# Copy migrations to migrations directory
COPY internal/core/infrastructure/storage/migrations ./migrations

# Default command: apply all pending migrations
# The path is relative to WORKDIR (/app), so file://migrations points to /app/migrations
CMD ["./migrate", "-command=up", "-path=file:///app/migrations"]

# =========================
# Production stage
# =========================
FROM alpine:3.19 AS production

RUN apk --no-cache add ca-certificates tzdata dumb-init && \
    addgroup -g 1001 -S app && \
    adduser -S app -u 1001 && \
    mkdir -p /app

WORKDIR /app

COPY --from=builder /app/main .
COPY --from=builder /app/config.yaml .

RUN chown -R app:app /app

USER app

EXPOSE 3010

ENTRYPOINT ["dumb-init", "--"]
CMD ["./main"]
