package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"quotes/internal/config"
	"quotes/internal/core/api/http"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage"
	"syscall"
	"time"
)

func main() {
	// Load configuration
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Connect to database
	db, err := storage.NewDB(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
	}()

	// Create HTTP application
	httpApp := http.NewApp(cfg, db.DB)

	// Create quotes collector job
	quotesCollector := jobs.NewQuotesCollector(cfg, db.DB)

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start quotes collection job
	quotesCollector.Start(ctx)

	// Start HTTP server in a goroutine
	go func() {
		if err := httpApp.Run(); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	// Stop quotes collector
	quotesCollector.Stop()

	// Give some time for graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Wait for shutdown or timeout
	select {
	case <-shutdownCtx.Done():
		log.Println("Shutdown timeout exceeded")
	default:
		log.Println("Server shutdown complete")
	}
}
