package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/api/http/handlers"
	httpmw "quotes/internal/core/api/http/middleware"
	apiprices "quotes/internal/core/application/prices"
	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
)

// AppDeps wires everything the HTTP server needs to come up. Pre-built repos and
// services are injected so main.go is the single point of construction.
type AppDeps struct {
	Config          *config.Config
	DB              *gorm.DB
	Logger          *zerolog.Logger
	TokenPriceQuery apiprices.QueryService
	RWAPriceQuery   apiprices.QueryService
	TokenPriceRepo  *repositories.TokenPriceRepository
	RWAPriceRepo    *repositories.RWAPriceRepository // for chart endpoints
	// TokenChangeRepo / RWAChangeRepo back the /change endpoints. Each
	// runs a single SQL per request via the existing CAs; no new tables.
	TokenChangeRepo *repositories.TokenChangeRepository
	RWAChangeRepo   *repositories.RWAChangeRepository
	// Optional, enables `?in=` multi-currency conversions on RWA endpoints.
	// When either field is nil the handler rejects `?in=` with 400.
	FXConverter apiprices.PriceConverter
	Lookup      *repositories.LookupRepository
	// TickerQuery powers /v1/tickers/:token/latest and /distribution. Nil
	// disables the routes silently (no handlers mounted).
	TickerQuery apitickers.QueryService
}

// App owns one or two HTTP servers:
//
//	publicServer   — :{server.port}, internet-facing. CORS + rate-limit on,
//	                 MBIO JWT middleware guards /v1/rwa/* and /v1/pairs/rwa.
//	internalServer — :{server.internal_port}, optional. No CORS, no rate-limit,
//	                 no RWA auth. Hosts /metrics. Reached only from inside the
//	                 cluster via a ClusterIP Service + NetworkPolicy.
//
// When server.internal_port is unset (local dev) internalServer is nil and
// /metrics is mounted on the public engine — the single-port legacy layout.
type App struct {
	config         *config.Config
	publicServer   *http.Server
	internalServer *http.Server
	logger         *zerolog.Logger
	readinessGate  *handlers.ReadinessGate
}

// NewApp builds the HTTP server(s). Side-effects: sets gin mode from config,
// creates engines, registers middleware, mounts routes, configures timeouts,
// builds the MBIO JWT middleware (when auth is enabled).
func NewApp(deps AppDeps) (*App, error) {
	logger := deps.Logger
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	cfg := deps.Config

	configureGinMode(cfg)

	appLogger := logging.WithComponent(logger, "http_app")
	gate := handlers.NewReadinessGate()
	routerDepsBase := buildRouterDeps(deps, cfg, gate)

	// MBIO JWT middleware. Built once, mounted only on the public engine. When
	// auth is disabled (auth.enabled=false, dev/CI only) the public listener
	// serves RWA routes without a token wrapper — same as the internal one.
	var rwaAuth gin.HandlerFunc
	if cfg.Auth.JWTVerificationEnabled() {
		mid, err := httpmw.MBIOJWT(&cfg.Auth, appLogger)
		if err != nil {
			return nil, err
		}
		rwaAuth = mid
	} else {
		appLogger.Warn().Msg("auth_disabled_rwa_routes_open_on_public_listener")
	}

	publicEngine := buildPublicEngine(cfg, appLogger)
	publicDeps := routerDepsBase
	publicDeps.RWAAuth = rwaAuth
	// Docs (Swagger UI + openapi.yaml) are mounted on the public engine in every
	// mode. Access to /docs and /openapi.yaml is gated at the infrastructure
	// layer (reverse proxy / network policy), not in the app — so we expose them
	// unconditionally here and let the edge decide who reaches them.
	publicDeps.MountDocs = true
	SetupRoutes(publicEngine, publicDeps)

	publicServer := &http.Server{
		Addr:              cfg.Server.Host + ":" + cfg.Server.Port,
		Handler:           publicEngine,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		WriteTimeout:      cfg.Server.WriteTimeout.D(),
		IdleTimeout:       cfg.Server.IdleTimeout.D(),
	}

	app := &App{
		config:        cfg,
		publicServer:  publicServer,
		logger:        appLogger,
		readinessGate: gate,
	}

	internalPort := strings.TrimSpace(cfg.Server.InternalPort)
	if internalPort == "" {
		// Single-port mode (local dev). /metrics stays on the public engine for
		// scrape continuity; the security note in app docs applies.
		publicEngine.GET("/metrics", gin.WrapH(promhttp.Handler()))
		return app, nil
	}

	internalEngine := buildInternalEngine(cfg, appLogger)
	internalDeps := routerDepsBase
	internalDeps.RWAAuth = nil
	internalDeps.MountDocs = true
	SetupRoutes(internalEngine, internalDeps)
	internalEngine.GET("/metrics", gin.WrapH(promhttp.Handler()))

	app.internalServer = &http.Server{
		Addr:              cfg.Server.Host + ":" + internalPort,
		Handler:           internalEngine,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		WriteTimeout:      cfg.Server.WriteTimeout.D(),
		IdleTimeout:       cfg.Server.IdleTimeout.D(),
	}
	return app, nil
}

func configureGinMode(cfg *config.Config) {
	switch strings.ToLower(strings.TrimSpace(cfg.Server.GinMode)) {
	case "debug":
		gin.SetMode(gin.DebugMode)
	case "release":
		gin.SetMode(gin.ReleaseMode)
	case "test":
		gin.SetMode(gin.TestMode)
	case "":
		h := strings.TrimSpace(cfg.Server.Host)
		if h == "localhost" || h == "127.0.0.1" {
			gin.SetMode(gin.DebugMode)
		} else {
			gin.SetMode(gin.ReleaseMode)
		}
	default:
		gin.SetMode(gin.ReleaseMode)
	}
}

// buildPublicEngine returns the engine used for the external-facing listener:
// full middleware stack, CORS allowlist, optional inbound rate limit.
func buildPublicEngine(cfg *config.Config, logger *zerolog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(logger))
	router.Use(httpmw.PrometheusHTTP())
	router.Use(gin.Recovery())
	router.Use(httpmw.CORS(cfg.Server.CORS))
	router.Use(httpmw.RateLimit(cfg.Server.RateLimit))
	if to := cfg.Server.HandlerTimeout.D(); to > 0 {
		router.Use(httpmw.HandlerTimeout(to))
	}
	if cfg.Server.PprofEnabled {
		httpmw.RegisterPprof(router)
	}
	return router
}

// buildInternalEngine returns the engine used for the intra-cluster listener.
// CORS and rate-limit are stripped: callers are trusted pods inside the cluster,
// not browsers or external clients. Logging, prometheus, recovery and per-handler
// timeout stay — they protect against runaway internal callers and keep metrics
// consistent.
func buildInternalEngine(cfg *config.Config, logger *zerolog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(logger))
	router.Use(httpmw.PrometheusHTTP())
	router.Use(gin.Recovery())
	if to := cfg.Server.HandlerTimeout.D(); to > 0 {
		router.Use(httpmw.HandlerTimeout(to))
	}
	return router
}

func buildRouterDeps(deps AppDeps, cfg *config.Config, gate *handlers.ReadinessGate) RouterDeps {
	tokenDeps := handlers.TokenPriceDeps{
		Service:       deps.TokenPriceQuery,
		Repo:          deps.TokenPriceRepo,
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      cfg.Server.MaxQueryLimit,
		DefaultLimit:  100,
	}
	// FA chart service runs over the existing TokenPriceRepository, which
	// already satisfies CandleRepository (see token_price_repository.go).
	// Converter is left nil — FA charts don't use ?in= (currency lookup
	// happens in SQL via quote_currency).
	tokenChartsDeps := handlers.TokenChartDeps{
		Charts: &apiprices.ChartService{
			Repo:     deps.TokenPriceRepo,
			Caps:     apiprices.DefaultCaps(),
			MaxLimit: cfg.Server.MaxQueryLimit,
			Kind:     "fa",
		},
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      cfg.Server.MaxQueryLimit,
		DefaultLimit:  100,
	}
	rwaDeps := handlers.RWAPriceDeps{
		Service:         deps.RWAPriceQuery,
		Converter:       deps.FXConverter,
		Lookup:          deps.Lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxLimit:        cfg.Server.MaxQueryLimit,
		DefaultLimit:    100,
		MaxInCurrencies: cfg.Server.MaxInCurrencies,
		// ath + price-one-year-ago for /latest; same concrete repo as charts.
		Stats: deps.RWAPriceRepo,
	}
	// RWA chart service runs over RWAPriceRepository. Converter enables
	// `?in=<currency>` close-of-bucket FX (see ADR-0015 / ADR-0013); when
	// FXConverter is nil at the AppDeps level (e.g. CoinGecko key absent
	// in dev) the chart handler 400s on `?in=` cleanly via preflight.
	rwaChartsDeps := handlers.RWAChartDeps{
		Charts: &apiprices.ChartService{
			Repo:      deps.RWAPriceRepo,
			Converter: deps.FXConverter,
			Caps:      apiprices.DefaultCaps(),
			MaxLimit:  cfg.Server.MaxQueryLimit,
			Kind:      "rwa",
		},
		Lookup:        deps.Lookup,
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      cfg.Server.MaxQueryLimit,
		DefaultLimit:  100,
	}
	// /change endpoints — per design Decision #5, one ChangeService per class
	// (FT and RWA), each with its own ChangeCache and Kind label so metrics
	// stay disambiguated. Service composes ChangeRepository + cache + singleflight.
	// Converter (Decision #19, post-FX-fix) enables `?in=usd,eur,...` on the
	// RWA change endpoint with at-or-before FX semantics.
	changeDeps := handlers.ChangeDeps{
		FTService: &apiprices.ChangeService{
			Repo:  deps.TokenChangeRepo,
			Cache: apiprices.NewChangeCache(),
			Kind:  "fa",
		},
		RWAService: &apiprices.ChangeService{
			Repo:  deps.RWAChangeRepo,
			Cache: apiprices.NewChangeCache(),
			Kind:  "rwa",
		},
		Lookup:          deps.Lookup,
		Converter:       deps.FXConverter,
		DefaultSource:   prices.SourceCoinGecko,
		RWASource:       prices.SourceEquiteez,
		MaxInCurrencies: cfg.Server.MaxInCurrencies,
	}
	rwaPairsDeps := handlers.RWAPairsDeps{Lookup: deps.Lookup}
	tickerDeps := handlers.TickerDeps{
		Service:          deps.TickerQuery,
		Converter:        deps.FXConverter,
		DefaultSource:    prices.SourceCoinGecko,
		MaxInCurrencies:  cfg.Server.MaxInCurrencies,
		TickerStaleAfter: time.Duration(cfg.Server.TickerStaleAfter),
	}
	// Legacy /quotes — restored for downstream services that still pin to
	// the v0.1.0 wide-format route. MVRK + CoinGecko only by design;
	// hard-coded rather than registry-looked-up so a missing tokens row
	// doesn't break server startup.
	legacyQuotesDeps := handlers.LegacyQuotesDeps{
		Repo:        repositories.NewLegacyQuoteRepository(deps.DB),
		TokenSymbol: "mvrk",
		SourceCode:  string(prices.SourceCoinGecko),
		MaxLimit:    cfg.Server.MaxQueryLimit,
	}
	return RouterDeps{
		DB:            deps.DB,
		ReadinessGate: gate,
		TokenPrice:    tokenDeps,
		TokenCharts:   tokenChartsDeps,
		RWAPrice:      rwaDeps,
		RWACharts:     rwaChartsDeps,
		RWAPairs:      rwaPairsDeps,
		Change:        changeDeps,
		LegacyQuotes:  legacyQuotesDeps,
		Ticker:        tickerDeps,
	}
}

// Run starts both listeners (or just the public one in single-port mode) and
// blocks until either returns. The first error from either server cancels the
// errgroup so the caller's Shutdown can drain both cleanly.
func (a *App) Run() error {
	g, _ := errgroup.WithContext(context.Background())
	g.Go(func() error {
		a.logger.Info().Str("addr", a.publicServer.Addr).Str("listener", "public").Msg("starting_http_server")
		if err := a.publicServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})
	if a.internalServer != nil {
		g.Go(func() error {
			a.logger.Info().Str("addr", a.internalServer.Addr).Str("listener", "internal").Msg("starting_http_server")
			if err := a.internalServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		})
	}
	return g.Wait()
}

// Shutdown gracefully stops both listeners in parallel. ctx is shared so a single
// deadline applies to the whole drain — typical caller passes 30s.
func (a *App) Shutdown(ctx context.Context) error {
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error { return a.publicServer.Shutdown(ctx) })
	if a.internalServer != nil {
		g.Go(func() error { return a.internalServer.Shutdown(ctx) })
	}
	return g.Wait()
}

// StartDraining flips /readyz to 503 so a load balancer pulls the pod out of
// rotation before Shutdown stops accepting connections. Idempotent.
func (a *App) StartDraining() {
	if a.readinessGate != nil {
		a.readinessGate.StartDraining()
	}
}
