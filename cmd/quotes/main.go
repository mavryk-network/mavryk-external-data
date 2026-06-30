// Command quotes is the entry point for the mavryk-external-data service.
//
// API documentation lives in [docs/openapi.yaml] (OpenAPI 3.0); the spec and
// Swagger UI are served at /openapi.yaml and /docs respectively. We removed
// swaggo/swag (which only emits Swagger 2.0) along with the dependency tree
// it pulled in — see docs/adr/0011-openapi-3-hand-written.md.
package main

import (
	"context"
	"errors"
	stdhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"quotes/internal/config"
	httpapp "quotes/internal/core/api/http"
	apiprices "quotes/internal/core/application/prices"
	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/httpclient"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
)

func main() {
	os.Exit(run())
}

// run is split out so defers (DB close, etc.) run on every exit path. log.Fatalf
// would skip them.
func run() int {
	logger := logging.NewLogger()

	cfg, err := config.Load("config.yaml")
	if err != nil {
		logger.Error().Err(err).Msg("config_load_failed")
		return 1
	}
	warnDevDefaults(cfg, logger)

	httpclient.ConfigureSharedTransport(httpclient.TransportSettings{
		MaxIdleConns:        cfg.API.TransportMaxIdleConns,
		MaxIdleConnsPerHost: cfg.API.TransportMaxIdleConnsPerHost,
		MaxConnsPerHost:     cfg.API.TransportMaxConnsPerHost,
		IdleConnTimeout:     cfg.API.TransportIdleConnTimeout.D(),
		TLSHandshakeTimeout: cfg.API.TransportTLSHandshakeTimeout.D(),
	})

	db, err := storage.NewDB(cfg, logger)
	if err != nil {
		logger.Error().Err(err).Msg("db_connect_failed")
		return 1
	}
	defer func() {
		if err := db.Close(); err != nil {
			logger.Error().Err(err).Msg("db_close_failed")
		}
	}()

	// Bootstrap: lookup-tables drive the runtime token/source registries. Without
	// this, prices.NewToken("mvrk") fails because the registry is empty.
	bootstrapCtx, bootstrapCancel := context.WithTimeout(context.Background(), 10*time.Second)
	lookup := repositories.NewLookupRepository(db.DB)
	tokens, err := lookup.Tokens(bootstrapCtx)
	bootstrapCancel()
	if err != nil {
		logger.Error().Err(err).Msg("registry_bootstrap_failed")
		return 1
	}
	if len(tokens) == 0 {
		logger.Warn().Msg("token_registry_empty_check_seed_migration")
	}
	prices.RegisterTokens(tokens)

	// Repositories: storage layer. Batch size from config (with safe fallback).
	batch := storage.BatchSize(cfg)
	tokenRepo := repositories.NewTokenPriceRepository(db.DB).WithBatchSize(batch)
	rwaRepo := repositories.NewRWAPriceRepository(db.DB).WithBatchSize(batch)
	stateRepo := repositories.NewBackfillStateRepository(db.DB)
	// Change repos for /change endpoints. Read-only; share the GORM handle.
	tokenChangeRepo := repositories.NewTokenChangeRepository(db.DB)
	rwaChangeRepo := repositories.NewRWAChangeRepository(db.DB)
	// Ticker repo + cache decorator. Two TTLs because /latest and /distribution
	// have different cost / call-rate profiles (see ADR-0007 / Q1).
	tickerRepo := repositories.NewTickerRepository(db.DB).WithBatchSize(batch)
	tickerAppRepo := apitickers.NewCachedRepository(
		tickerRepo,
		time.Duration(cfg.Tickers.Cache.LatestTTLSeconds)*time.Second,
		time.Duration(cfg.Tickers.Cache.DistributionTTLSeconds)*time.Second,
	)

	// Application repositories: cache decorator on top, used by HTTP and by jobs
	// (jobs write through, decorator invalidates).
	cacheTTL := time.Duration(cfg.Server.LatestQuoteCacheTTLSeconds) * time.Second
	tokenAppRepo := apiprices.NewCachedRepository(tokenRepoAdapter{tokenRepo}, cacheTTL)
	rwaAppRepo := apiprices.NewCachedRepository(rwaRepoAdapter{rwaRepo}, cacheTTL)

	// FX converter for RWA `?in=` multi-currency conversions. Reads from
	// the same `token_prices` series the live job populates. Honours the
	// `ts` argument via at-or-before semantics — historical chart
	// conversions get the rate that was current at bucket time, not
	// today's rate (Decision #19; fix_todo.md).
	fxConverter := apiprices.NewTokenFXConverter(
		fxRepoAdapter{tokenRepo},
		time.Duration(cfg.Server.FXMaxStalenessSeconds)*time.Second,
		prices.SourceCoinGecko,
	)

	httpApp, err := httpapp.NewApp(httpapp.AppDeps{
		Config:          cfg,
		DB:              db.DB,
		Logger:          logger,
		TokenPriceQuery: tokenAppRepo,
		RWAPriceQuery:   rwaAppRepo,
		TokenPriceRepo:  tokenRepo,
		RWAPriceRepo:    rwaRepo,
		TokenChangeRepo: tokenChangeRepo,
		RWAChangeRepo:   rwaChangeRepo,
		FXConverter:     fxConverter,
		Lookup:          lookup,
		TickerQuery:     tickerAppRepo,
	})
	if err != nil {
		logger.Error().Err(err).Msg("http_app_init_failed")
		return 1
	}

	liveJob := jobs.NewCoinGeckoLiveJob(cfg, tokenAppRepo, logger)
	backfillJob := jobs.NewCoinGeckoBackfillJob(cfg, tokenAppRepo, tokenRepo, stateRepo, logger)
	rwaJob := jobs.NewEquiteezRWAJob(cfg, rwaAppRepo, lookup, logger)
	rwaBackfillJob := jobs.NewEquiteezBackfillJob(cfg, rwaAppRepo, lookup, stateRepo, logger)
	tickersJob := jobs.NewCoinGeckoTickersJob(cfg, tickerAppRepo, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopPoolCollector := metrics.StartDBPoolCollector(ctx, db.DB, 15*time.Second)
	defer stopPoolCollector()

	serverErrors := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error().Interface("panic", r).Msg("http_server_panic")
				serverErrors <- errors.New("http server panic")
			}
		}()
		if err := httpApp.Run(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	liveJob.Start(ctx)
	backfillJob.Start(ctx)

	// Discovery: pull the current Equiteez allowlist into rwa_pairs before the
	// collector reads from it. Failure here logs but doesn't block startup —
	// the collector falls back to whatever is already in DB (operator's manual
	// rows, previous sync). When `rwa.enabled=false` SyncRWAPairs is a no-op.
	syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
	if _, err := jobs.SyncRWAPairs(syncCtx, cfg, lookup, logger); err != nil {
		logger.Error().Err(err).Msg("rwa_pair_sync_failed_continuing")
	}
	syncCancel()

	rwaJob.Start(ctx)
	rwaBackfillJob.Start(ctx)
	tickersJob.Start(ctx)

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

	logger.Info().Msg("shutting_down")
	// Phase 1 — drain: flip /readyz to 503 so a load balancer pulls us out of
	// rotation before we stop accepting new connections.
	httpApp.StartDraining()
	if cfg.Server.ShutdownDrainSeconds > 0 {
		drain := time.Duration(cfg.Server.ShutdownDrainSeconds) * time.Second
		logger.Info().Dur("drain", drain).Msg("readyz_draining")
		select {
		case <-time.After(drain):
		case <-quit:
			// second signal — abort the drain
		}
	}
	// Phase 2 — stop background work and HTTP server.
	cancel()
	liveJob.Stop()
	backfillJob.Stop()
	rwaJob.Stop()
	rwaBackfillJob.Stop()
	tickersJob.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpApp.Shutdown(shutdownCtx); err != nil {
		logger.Error().Err(err).Msg("http_shutdown_error")
	} else {
		logger.Info().Msg("http_shutdown_complete")
	}

	if listenErr != nil {
		return 1
	}
	return 0
}

// warnDevDefaults logs warnings when production-looking config (gin_mode=release)
// still leans on dev defaults that wouldn't survive an audit. Hard rejects (e.g.
// default postgres password) live in Validate; here we only nudge.
func warnDevDefaults(cfg *config.Config, logger *zerolog.Logger) {
	if !strings.EqualFold(strings.TrimSpace(cfg.Server.GinMode), "release") {
		return
	}
	if cfg.Server.RateLimit.RPS <= 0 {
		logger.Warn().Msg("inbound_rate_limit_disabled_in_release_mode")
	}
}

// tokenRepoAdapter and rwaRepoAdapter let the concrete repos satisfy
// apiprices.Repository without importing the application package.
type tokenRepoAdapter struct {
	r *repositories.TokenPriceRepository
}

func (a tokenRepoAdapter) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	return a.r.Save(ctx, points)
}
func (a tokenRepoAdapter) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	return a.r.Query(ctx, q)
}

// fxRepoAdapter satisfies apiprices.HistoricalFXSource without leaking the
// concrete repository type into the application package. Forwards directly
// to TokenPriceRepository.LatestRateAtOrBefore (Decision #19).
type fxRepoAdapter struct {
	r *repositories.TokenPriceRepository
}

func (a fxRepoAdapter) LatestRateAtOrBefore(
	ctx context.Context,
	source prices.Source,
	tokenSymbol string,
	quoteCurrency string,
	at time.Time,
) (prices.PricePoint, bool, error) {
	return a.r.LatestRateAtOrBefore(ctx, source, tokenSymbol, quoteCurrency, at)
}

type rwaRepoAdapter struct {
	r *repositories.RWAPriceRepository
}

func (a rwaRepoAdapter) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	return a.r.Save(ctx, points)
}
func (a rwaRepoAdapter) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	return a.r.Query(ctx, q)
}
