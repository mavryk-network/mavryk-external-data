# =========================
# Builder stage
# =========================
# refactoring_v2 §8.1: pin base images by digest in production deployments.
# Recommended setup: enable Renovate or Dependabot to auto-update the digest;
# the simplest manual refresh is `docker pull golang:1.25-alpine && docker inspect`
# then replace the tag with `golang:1.25-alpine@sha256:<digest>`.
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

ARG GIT_SHA=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags "-w -s -X main.Version=${GIT_SHA}" \
        -o main cmd/quotes/main.go

# =========================
# Migration stage
# =========================
FROM alpine:3.22 AS migration

WORKDIR /app

# Install postgresql-client for running migrations, and create an unprivileged
# user — nothing in the migration flow needs root.
RUN apk add --no-cache postgresql-client && \
    addgroup -g 1001 -S app && \
    adduser -S app -u 1001

COPY migrations ./migrations

# Copy migration script
COPY scripts/run-migrations.sh ./run-migrations.sh
RUN chmod +x ./run-migrations.sh

USER app

# Default command: run migration script
CMD ["./run-migrations.sh"]

# =========================
# Production stage
# =========================
FROM alpine:3.22 AS production

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
