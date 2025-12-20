# =========================
# Builder stage
# =========================
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main cmd/quotes/main.go

# =========================
# Migration stage
# =========================
FROM alpine:3.19 AS migration

WORKDIR /app

# Install postgresql-client for running migrations
RUN apk add --no-cache postgresql-client

# Copy migrations to migrations directory
COPY internal/core/infrastructure/storage/migrations ./migrations

# Copy migration script
COPY scripts/run-migrations.sh ./run-migrations.sh
RUN chmod +x ./run-migrations.sh

# Default command: run migration script
CMD ["./run-migrations.sh"]

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
