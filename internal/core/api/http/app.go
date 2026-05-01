package http

import (
	"net/http"
	"strings"

	"quotes/internal/config"
	"quotes/internal/core/api/http/handlers"
	httpmw "quotes/internal/core/api/http/middleware"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
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
	// Optional, enables `?in=` multi-currency conversions on RWA endpoints.
	// When either field is nil the handler rejects `?in=` with 400.
	FXConverter apiprices.PriceConverter
	Lookup      *repositories.LookupRepository
}

type App struct {
	config        *config.Config
	server        *http.Server
	logger        *zerolog.Logger
	readinessGate *handlers.ReadinessGate
}

// NewApp builds the HTTP server. Side-effects: sets gin mode from config, creates
// the engine, registers middleware, mounts routes, configures timeouts.
func NewApp(deps AppDeps) *App {
	logger := deps.Logger
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	cfg := deps.Config

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

	appLogger := logging.WithComponent(logger, "http_app")

	router := gin.New()
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(appLogger))
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

	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	gate := handlers.NewReadinessGate()

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
	}
	SetupRoutes(router, RouterDeps{
		DB:            deps.DB,
		ReadinessGate: gate,
		TokenPrice:    tokenDeps,
		TokenCharts:   tokenChartsDeps,
		RWAPrice:      rwaDeps,
	})

	server := &http.Server{
		Addr:              cfg.Server.Host + ":" + cfg.Server.Port,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		WriteTimeout:      cfg.Server.WriteTimeout.D(),
		IdleTimeout:       cfg.Server.IdleTimeout.D(),
	}

	return &App{config: cfg, server: server, logger: appLogger, readinessGate: gate}
}

func (a *App) Run() error {
	a.logger.Info().Str("addr", a.server.Addr).Msg("starting_http_server")
	return a.server.ListenAndServe()
}

// Server returns the underlying HTTP server for graceful shutdown.
func (a *App) Server() *http.Server { return a.server }

// StartDraining flips /readyz to 503 so a load balancer pulls the pod out of
// rotation before Server.Shutdown stops accepting connections. Idempotent.
func (a *App) StartDraining() {
	if a.readinessGate != nil {
		a.readinessGate.StartDraining()
	}
}
