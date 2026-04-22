// @title           Mavryk External Data API
// @version         1.0
// @description     High-performance Go service for collecting and serving cryptocurrency quotes (MVRK, USDT, and more)
// @termsOfService  http://swagger.io/terms/

// @contact.name   API Support
// @contact.url    http://www.swagger.io/support
// @contact.email  support@swagger.io

// @license.name  Apache 2.0
// @license.url   http://www.apache.org/licenses/LICENSE-2.0.html

// @BasePath  /
// @schemes   http https
// (No fixed @host — Swagger UI calls the same origin as /swagger, so Try it works over port-forward and on non-localhost URLs.)

package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"quotes/internal/config"
	"quotes/internal/core/api/http"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/responsecache"
	"quotes/internal/core/infrastructure/storage"
	"quotes/internal/logging"
	"syscall"
	"time"

	_ "quotes/docs" // Swagger documentation
)

func main() {
	os.Exit(run())
}

// run contains the full process lifecycle so defers (e.g. DB close) always run and
// HTTP listen errors can exit with code 1 without log.Fatalf (which would skip defers).
func run() int {
	logger := logging.NewLogger()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		logger.Error().Err(err).Msg("failed_to_load_configuration")
		return 1
	}

	db, err := storage.NewDB(cfg, logger)
	if err != nil {
		logger.Error().Err(err).Msg("failed_to_connect_database")
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error().Err(err).Msg("database_close_error")
		}
	}()

	var quoteResponseCache *responsecache.Cache
	if cfg.Server.LatestQuoteCacheTTLSeconds > 0 {
		quoteResponseCache = responsecache.New(time.Duration(cfg.Server.LatestQuoteCacheTTLSeconds) * time.Second)
	}

	httpApp := http.NewApp(cfg, db.DB, logger, quoteResponseCache)

	quotesCollector := jobs.NewQuotesCollector(cfg, db.DB, logger, quoteResponseCache)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErrors := make(chan error, 1)
	go func() {
		err := httpApp.Run()
		if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	quotesCollector.Start(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	var listenErr error
	select {
	case sig := <-quit:
		logger.Info().Str("signal", sig.String()).Msg("shutdown_signal_received")
	case err := <-serverErrors:
		listenErr = err
		logger.Error().Err(err).Msg("http_server_failed")
	}

	logger.Info().Msg("shutting_down_server")

	// Cancel background work first; collectors observe ctx.Done() and exit their loops.
	cancel()
	quotesCollector.Stop() // waits until all token collector goroutines return

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpApp.Server().Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("http_server_shutdown_error")
	} else {
		logger.Info().Msg("http_server_shutdown_complete")
	}

	if listenErr != nil {
		return 1
	}
	return 0
}
