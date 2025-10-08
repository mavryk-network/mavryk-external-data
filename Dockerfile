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
FROM alpine:3.19 AS migration

RUN apk add --no-cache postgresql-client bash curl make

WORKDIR /app

COPY Makefile ./
COPY internal/core/infrastructure/storage/migrations ./internal/core/infrastructure/storage/migrations

CMD ["make", "migrate-up"]

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
