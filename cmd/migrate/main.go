package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	var (
		command     = flag.String("command", "up", "Migration command: up, down, force, version")
		steps       = flag.Int("steps", 0, "Number of migration steps (0 = all)")
		version     = flag.Int("version", 0, "Version for force command")
		dsn         = flag.String("dsn", "", "Database connection string (postgres://user:pass@host:port/dbname?sslmode=disable)")
		migrationsPath = flag.String("path", "file://internal/core/infrastructure/storage/migrations", "Path to migrations directory")
	)
	flag.Parse()

	// Build DSN from environment if not provided
	if *dsn == "" {
		host := getEnv("POSTGRES_HOST", "localhost")
		port := getEnv("POSTGRES_PORT", "5432")
		user := getEnv("POSTGRES_USER", "postgres")
		password := getEnv("POSTGRES_PASSWORD", "postgres")
		database := getEnv("POSTGRES_DATABASE", "quotes")
		*dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			user, password, host, port, database)
	}

	// Handle migrations path
	var absPath string
	if len(*migrationsPath) > 7 && (*migrationsPath)[:7] == "file://" {
		// Already has file:// prefix, extract path
		path := (*migrationsPath)[7:]
		abs, err := filepath.Abs(path)
		if err != nil {
			log.Fatalf("Failed to get absolute path: %v", err)
		}
		absPath = "file://" + abs
	} else {
		// No file:// prefix, add it
		abs, err := filepath.Abs(*migrationsPath)
		if err != nil {
			log.Fatalf("Failed to get absolute path: %v", err)
		}
		absPath = "file://" + abs
	}

	m, err := migrate.New(absPath, *dsn)
	if err != nil {
		log.Fatalf("Failed to create migrate instance: %v", err)
	}
	defer func() {
		sourceErr, databaseErr := m.Close()
		if sourceErr != nil {
			log.Printf("Error closing migrate source: %v", sourceErr)
		}
		if databaseErr != nil {
			log.Printf("Error closing migrate database: %v", databaseErr)
		}
	}()

	versionNum, dirty, err := m.Version()
	if err != nil && err != migrate.ErrNilVersion {
		log.Fatalf("Failed to get version: %v", err)
	}

	switch *command {
	case "up":
		if *steps > 0 {
			err = m.Steps(*steps)
		} else {
			err = m.Up()
		}
		if err != nil && err != migrate.ErrNoChange {
			log.Fatalf("Failed to apply migrations: %v", err)
		}
		if err == migrate.ErrNoChange {
			log.Println("No migrations to apply")
		} else {
			log.Println("Migrations applied successfully")
		}

	case "down":
		if *steps > 0 {
			err = m.Steps(-*steps)
		} else {
			log.Fatalf("Down command requires -steps parameter for safety")
		}
		if err != nil && err != migrate.ErrNoChange {
			log.Fatalf("Failed to rollback migrations: %v", err)
		}
		log.Println("Migrations rolled back successfully")

	case "force":
		if *version == 0 {
			log.Fatalf("Force command requires -version parameter")
		}
		err = m.Force(*version)
		if err != nil {
			log.Fatalf("Failed to force version: %v", err)
		}
		log.Printf("Forced version to %d", *version)

	case "version":
		if err == migrate.ErrNilVersion {
			log.Println("No migrations applied yet")
		} else {
			log.Printf("Current version: %d (dirty: %v)", versionNum, dirty)
		}

	case "create":
		name := flag.Arg(0)
		if name == "" {
			log.Fatalf("Create command requires migration name: migrate -command=create <name>")
		}
		createMigration(name, *migrationsPath)

	default:
		log.Fatalf("Unknown command: %s. Use: up, down, force, version, create", *command)
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func createMigration(name, migrationsPath string) {
	// Remove file:// prefix if present
	path := migrationsPath
	if len(migrationsPath) > 7 && migrationsPath[:7] == "file://" {
		path = migrationsPath[7:]
	}

	// Get next migration number
	files, err := filepath.Glob(filepath.Join(path, "*.up.sql"))
	if err != nil {
		log.Fatalf("Failed to list migrations: %v", err)
	}

	nextNum := len(files) + 1
	version := fmt.Sprintf("%04d", nextNum)

	upFile := filepath.Join(path, fmt.Sprintf("%s_%s.up.sql", version, name))
	downFile := filepath.Join(path, fmt.Sprintf("%s_%s.down.sql", version, name))

	// Create up migration
	upContent := fmt.Sprintf("-- +migrate Up\n-- Migration: %s\n\n", name)
	if err := os.WriteFile(upFile, []byte(upContent), 0644); err != nil {
		log.Fatalf("Failed to create up migration: %v", err)
	}

	// Create down migration
	downContent := fmt.Sprintf("-- +migrate Down\n-- Rollback: %s\n\n", name)
	if err := os.WriteFile(downFile, []byte(downContent), 0644); err != nil {
		log.Fatalf("Failed to create down migration: %v", err)
	}

	log.Printf("Created migrations:\n  %s\n  %s", upFile, downFile)
}

