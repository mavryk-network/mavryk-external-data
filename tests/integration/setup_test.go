//go:build integration

// Package integration hosts test-suite scaffolding for tests that need a real
// TimescaleDB instance — currently the chart-repository OHLC reads against
// continuous aggregates. Mirrors mavryk-wallet-backend/tests/integration/
// patterns: a single container per package + a tcp/sql warmup, Postgres-only
// (no Redis here).
//
// Build tag `integration` keeps `go test ./...` Docker-free; CI / Makefile
// runs `go test -tags=integration ./tests/integration/...` explicitly.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for warmup + migrations.
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	testPGUser = "postgres"
	testPGPass = "postgres"
	testPGDB   = "quotes_test"

	// timescaleImage matches docker-compose.yml default. Pinning to a major
	// version (pg16) keeps the test parity with prod baseline.
	timescaleImage = "timescale/timescaledb:latest-pg16"
)

// pgDSN is the DSN every integration test connects with — populated by
// TestMain and reused by gorm helpers in db_helper_test.go.
var pgDSN string

// pgContainer is held in the package scope so deferred cleanup runs even on
// panic during m.Run().
var pgContainer testcontainers.Container

// loopbackHost forces IPv4 loopback for Docker-published ports. On GitHub
// Actions, "localhost" often resolves to [::1] while port mapping is
// IPv4-only, which yields "connection reset by peer" / EOF on first ping.
// Direct copy from mavryk-wallet-backend/tests/integration/setup_test.go.
func loopbackHost(host string) string {
	switch host {
	case "localhost", "::1", "[::1]":
		return "127.0.0.1"
	default:
		return host
	}
}

// waitPostgresAcceptsConnections retries until pgx (via database/sql) can
// complete PingContext. CI sometimes opens TCP before Postgres finishes
// startup; the container's own healthcheck races against test code.
func waitPostgresAcceptsConnections(ctx context.Context, dsn string) error {
	var lastErr error
	for i := 0; i < 60; i++ {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		_ = db.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timeout")
	}
	return fmt.Errorf("postgres did not accept connections: %w", lastErr)
}

// findMigrationsDir resolves the absolute path to migrations/ from any test
// working directory. Tests under tests/integration/ run with cwd =
// tests/integration; the dir is two levels up.
func findMigrationsDir() (string, error) {
	candidates := []string{"migrations", "../migrations", "../../migrations"}
	for _, p := range candidates {
		if abs, err := filepath.Abs(p); err == nil {
			if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("migrations/ directory not found relative to %s",
		mustGetwd())
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

// applyMigrations runs every `migrations/*.sql` file in lexical order via
// pgx simple-query mode. Mirrors `make migrate-up` (psql loop) so the test
// schema is byte-identical to dev/prod.
func applyMigrations(ctx context.Context, dsn string) error {
	dir, err := findMigrationsDir()
	if err != nil {
		return err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return fmt.Errorf("no migrations found in %s", dir)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db for migrations: %w", err)
	}
	defer db.Close()

	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

func TestMain(m *testing.M) {
	os.Exit(setupAndRun(m))
}

func setupAndRun(m *testing.M) int {
	ctx := context.Background()

	pg, err := tcpostgres.Run(ctx, timescaleImage,
		tcpostgres.WithDatabase(testPGDB),
		tcpostgres.WithUsername(testPGUser),
		tcpostgres.WithPassword(testPGPass),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timescaledb container: %v\n", err)
		return 1
	}
	pgContainer = pg
	defer func() {
		if termErr := pg.Terminate(ctx); termErr != nil {
			fmt.Fprintf(os.Stderr, "terminate timescaledb: %v\n", termErr)
		}
	}()

	host, err := pg.Host(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "container host: %v\n", err)
		return 1
	}
	host = loopbackHost(host)
	port, err := pg.MappedPort(ctx, "5432")
	if err != nil {
		fmt.Fprintf(os.Stderr, "container mapped port: %v\n", err)
		return 1
	}

	pgDSN = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port.Port(), testPGUser, testPGPass, testPGDB)

	// Belt-and-suspenders: TCP-level handshake before pgx-level Ping. Same
	// double-check as mavryk-wallet-backend; covers the gap between
	// container "ready" event and Postgres accepting connections.
	for i := 0; i < 15; i++ {
		conn, err := net.DialTimeout("tcp", host+":"+port.Port(), 2*time.Second)
		if err == nil {
			_ = conn.Close()
			break
		}
		if i == 14 {
			fmt.Fprintf(os.Stderr, "postgres tcp not ready: %v\n", err)
			return 1
		}
		time.Sleep(500 * time.Millisecond)
	}

	if err := waitPostgresAcceptsConnections(ctx, pgDSN); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	if err := applyMigrations(ctx, pgDSN); err != nil {
		fmt.Fprintf(os.Stderr, "apply migrations: %v\n", err)
		return 1
	}

	fmt.Printf("TimescaleDB ready at %s:%s; migrations applied\n", host, port.Port())
	return m.Run()
}
