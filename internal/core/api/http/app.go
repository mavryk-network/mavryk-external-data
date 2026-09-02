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

// AppDeps wires everything the HTTP server needs to come up.
type AppDeps struct {
	Config          *config.Config
	DB              *gorm.DB
	Logger          *zerolog.Logger
	TokenPriceQuery apiprices.QueryService
	RWAPriceQuery   apiprices.QueryService
	TokenPriceRepo  *repositories.TokenPriceRepository
	RWAPriceRepo    *repositories.RWAPriceRepository // for chart endpoints
	// TokenChangeRepo / RWAChangeRepo back the /change endpoints.
	TokenChangeRepo *repositories.TokenChangeRepository
	RWAChangeRepo   *repositories.RWAChangeRepository
	// Optional; when either field is nil the handler rejects `?in=` with 400.
	FXConverter apiprices.PriceConverter
	Lookup      *repositories.LookupRepository
	// Optional: surfaces primary-issuance assets on GET /v1/rwa; nil omits them.
	LaunchRepo *repositories.LaunchRepository
	// Nil disables the ticker routes (no handlers mounted).
	TickerQuery apitickers.QueryService
}

// App owns the internet-facing publicServer (rate-limited, MBIO JWT on
// /v1/rwa/*) and — when server.internal_port is set — an intra-cluster
// internalServer that hosts /metrics with neither. CORS is handled at the edge
// (Envoy Gateway), not here.
type App struct {
	config         *config.Config
	publicServer   *http.Server
	internalServer *http.Server
	logger         *zerolog.Logger
	readinessGate  *handlers.ReadinessGate
}

// NewApp builds the HTTP server(s).
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

	// Built once, public engine only. auth.enabled=false is meant for dev/CI but
	// is not refused at startup in any gin mode: it leaves the RWA routes
	// unwrapped on the public listener, and the warn below is the only signal.
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
	// Access to /docs and /openapi.yaml is gated at the edge (reverse proxy /
	// network policy), not in the app.
	publicDeps.MountDocs = true
	SetupRoutes(publicEngine, publicDeps)

	publicServer := &http.Server{
		Addr:              cfg.Server.Host + ":" + cfg.Server.Port,
		Handler:           publicEngine,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		WriteTimeout:      cfg.Server.WriteTimeout.D(),
		IdleTimeout:       cfg.Server.IdleTimeout.D(),
		MaxHeaderBytes:    maxHeaderBytes,
	}

	app := &App{
		config:        cfg,
		publicServer:  publicServer,
		logger:        appLogger,
		readinessGate: gate,
	}

	internalPort := strings.TrimSpace(cfg.Server.InternalPort)
	if internalPort == "" {
		// /metrics on the public engine leaks internal operational detail to
		// anonymous callers: allow it outside release mode only. A release-mode
		// deploy must set SERVER_INTERNAL_PORT to get metrics at all.
		if gin.Mode() == gin.ReleaseMode {
			appLogger.Warn().Msg("metrics_not_exposed_public_release_single_port_set_internal_port")
		} else {
			publicEngine.GET("/metrics", gin.WrapH(promhttp.Handler()))
		}
		if cfg.Server.PprofEnabled {
			appLogger.Warn().Msg("pprof_requires_internal_listener_set_internal_port")
		}
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
		MaxHeaderBytes:    maxHeaderBytes,
	}
	return app, nil
}

// maxHeaderBytes bounds request header size (net/http defaults to 1 MiB), capping
// the attacker-controlled bytes forced into every log line.
const maxHeaderBytes = 64 << 10 // 64 KiB

// launchLister adapts an optional *LaunchRepository to the handler interface.
// A typed-nil pointer stored in an interface is non-nil, so nil must be
// translated to a nil interface explicitly.
func launchLister(r *repositories.LaunchRepository) handlers.RWALaunchLister {
	if r == nil {
		return nil
	}
	return r
}

// launchResolver is launchLister's per-symbol counterpart.
func launchResolver(r *repositories.LaunchRepository) handlers.RWALaunchResolver {
	if r == nil {
		return nil
	}
	return r
}

func configureGinMode(cfg *config.Config) {
	switch cfg.Server.EffectiveGinMode() {
	case "debug":
		gin.SetMode(gin.DebugMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.ReleaseMode)
	}
}

// configureTrustedProxies applies server.trusted_proxies. Empty (the default)
// trusts NO proxy: gin's own default trusts everything, so c.ClientIP() would
// honor a client-supplied X-Forwarded-For and let an attacker spoof a fresh IP
// per request, bypassing the per-IP rate limiter.
func configureTrustedProxies(router *gin.Engine, cfg *config.Config, logger *zerolog.Logger) {
	// Trim to match Validate, which checks TrimSpace(entry): a padded YAML entry
	// (" 10.0.0.0/8") passes config validation but gin rejects it as invalid CIDR.
	proxies := make([]string, 0, len(cfg.Server.TrustedProxies))
	for _, p := range cfg.Server.TrustedProxies {
		if p = strings.TrimSpace(p); p != "" {
			proxies = append(proxies, p)
		}
	}
	if len(proxies) == 0 {
		_ = router.SetTrustedProxies(nil)
		return
	}
	if err := router.SetTrustedProxies(proxies); err != nil {
		// Fall back closed rather than trusting everything.
		logger.Error().Err(err).Strs("trusted_proxies", proxies).
			Msg("invalid_trusted_proxies_falling_back_to_none")
		_ = router.SetTrustedProxies(nil)
	}
}

// buildPublicEngine returns the external-facing engine: full middleware stack
// plus the optional inbound rate limit.
func buildPublicEngine(cfg *config.Config, logger *zerolog.Logger) *gin.Engine {
	router := gin.New()
	configureTrustedProxies(router, cfg, logger)
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(logger))
	router.Use(httpmw.PrometheusHTTP())
	router.Use(gin.Recovery())
	router.Use(httpmw.RateLimit(cfg.Server.RateLimit))
	if to := cfg.Server.HandlerTimeout.D(); to > 0 {
		router.Use(httpmw.HandlerTimeout(to))
	}
	return router
}

// buildInternalEngine returns the intra-cluster engine. The rate limit is
// stripped: callers are trusted pods, not browsers.
func buildInternalEngine(cfg *config.Config, logger *zerolog.Logger) *gin.Engine {
	router := gin.New()
	configureTrustedProxies(router, cfg, logger)
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(logger))
	router.Use(httpmw.PrometheusHTTP())
	router.Use(gin.Recovery())
	// pprof discloses stack traces and lets anyone pin a CPU for 30s — internal
	// listener only. Registered BEFORE the handler timeout so a long profile is
	// not truncated (gin applies Use() only to routes registered after it).
	if cfg.Server.PprofEnabled {
		httpmw.RegisterPprof(router)
	}
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
	// Converter stays nil: FA charts don't use ?in= — currency lookup happens
	// in SQL via quote_currency.
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
		// Serves primary-market assets, which have no orderbook pair.
		Launches: launchResolver(deps.LaunchRepo),
	}
	// Shared by the per-symbol chart handlers and the /v1/rwa overview list.
	// Converter enables `?in=` close-of-bucket FX (ADR-0015 / ADR-0013); nil
	// makes the chart handler 400 on `?in=` via preflight.
	rwaChartService := &apiprices.ChartService{
		Repo:      deps.RWAPriceRepo,
		Converter: deps.FXConverter,
		Caps:      apiprices.DefaultCaps(),
		MaxLimit:  cfg.Server.MaxQueryLimit,
		Kind:      "rwa",
	}
	rwaChartsDeps := handlers.RWAChartDeps{
		Charts:        rwaChartService,
		Lookup:        deps.Lookup,
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      cfg.Server.MaxQueryLimit,
		DefaultLimit:  100,
	}
	// One ChangeService per class (Decision #5) so metrics stay disambiguated.
	// The RWA one is shared with the /v1/rwa overview list so both use its cache.
	rwaChangeService := &apiprices.ChangeService{
		Repo:  deps.RWAChangeRepo,
		Cache: apiprices.NewChangeCache(),
		Kind:  "rwa",
	}
	changeDeps := handlers.ChangeDeps{
		FTService: &apiprices.ChangeService{
			Repo:  deps.TokenChangeRepo,
			Cache: apiprices.NewChangeCache(),
			Kind:  "fa",
		},
		RWAService:      rwaChangeService,
		Lookup:          deps.Lookup,
		Converter:       deps.FXConverter,
		DefaultSource:   prices.SourceCoinGecko,
		RWASource:       prices.SourceEquiteez,
		MaxInCurrencies: cfg.Server.MaxInCurrencies,
	}
	// GET /v1/pairs/rwa — orderbook pairs unioned with primary-issuance launches.
	rwaPairsDeps := handlers.RWAPairsDeps{
		Lookup:   deps.Lookup,
		Launches: launchLister(deps.LaunchRepo),
		Source:   prices.SourceEquiteez,
	}
	// GET /v1/rwa — market-overview list; the 5s response cache amortises the
	// per-asset fan-out across dashboard polls.
	rwaOverviewDeps := handlers.NewRWAOverviewDeps(
		deps.Lookup,
		launchLister(deps.LaunchRepo),
		rwaChangeService,
		rwaChartService,
		deps.FXConverter,
		prices.SourceEquiteez,
		5*time.Second,
	)
	tickerDeps := handlers.TickerDeps{
		Service:          deps.TickerQuery,
		Converter:        deps.FXConverter,
		DefaultSource:    prices.SourceCoinGecko,
		MaxInCurrencies:  cfg.Server.MaxInCurrencies,
		TickerStaleAfter: time.Duration(cfg.Server.TickerStaleAfter),
	}
	// Legacy /quotes — frozen v0.1.0 route. MVRK + CoinGecko are hard-coded
	// rather than registry-looked-up so a missing tokens row can't break startup.
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
		RWAOverview:   rwaOverviewDeps,
		Change:        changeDeps,
		LegacyQuotes:  legacyQuotesDeps,
		Ticker:        tickerDeps,
	}
}

// Run starts the listener(s) and blocks until one of them returns.
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

// Shutdown gracefully stops both listeners in parallel under one shared deadline.
func (a *App) Shutdown(ctx context.Context) error {
	g, _ := errgroup.WithContext(ctx)
	g.Go(func() error { return a.publicServer.Shutdown(ctx) })
	if a.internalServer != nil {
		g.Go(func() error { return a.internalServer.Shutdown(ctx) })
	}
	return g.Wait()
}

// StartDraining flips /readyz to 503 so a load balancer pulls the pod out of
// rotation before Shutdown. Idempotent.
func (a *App) StartDraining() {
	if a.readinessGate != nil {
		a.readinessGate.StartDraining()
	}
}
