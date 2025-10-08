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
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	db, err := storage.NewDB(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v", err)
		}
	}()

	httpApp := http.NewApp(cfg, db.DB)

	quotesCollector := jobs.NewQuotesCollector(cfg, db.DB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start HTTP server first so it's available during backfill
	go func() {
		if err := httpApp.Run(); err != nil {
			log.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Start quotes collector (may perform backfill)
	quotesCollector.Start(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	quotesCollector.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	select {
	case <-shutdownCtx.Done():
		log.Println("Shutdown timeout exceeded")
	default:
		log.Println("Server shutdown complete")
	}
}
