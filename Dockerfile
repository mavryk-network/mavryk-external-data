# =========================
# Builder stage
# =========================
# Base images are digest-pinned (multi-arch index digests) so a build always
# resolves the exact reviewed content; Renovate refreshes them
# (helpers:pinGitHubActionDigests / docker pinDigests in .github/renovate.json).
# Manual refresh: docker buildx imagetools inspect <tag> → replace the digest.
FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS builder

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
FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS migration

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
FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce AS production

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
