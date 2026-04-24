package http

import (
	"net/http"
	"strings"

	"quotes/internal/config"
	httpmw "quotes/internal/core/api/http/middleware"
	httpGetAll "quotes/internal/core/api/http/quotes/get_all"
	httpGetByToken "quotes/internal/core/api/http/quotes/get_by_token"
	httpGetCount "quotes/internal/core/api/http/quotes/get_count"
	httpGetLatest "quotes/internal/core/api/http/quotes/get_latest"
	appGetAll "quotes/internal/core/application/quotes/get_all"
	appGetByToken "quotes/internal/core/application/quotes/get_by_token"
	appGetCount "quotes/internal/core/application/quotes/get_count"
	appGetLatest "quotes/internal/core/application/quotes/get_latest"
	"quotes/internal/core/infrastructure/responsecache"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type App struct {
	config *config.Config
	db     *gorm.DB
	server *http.Server
	logger *zerolog.Logger
}

func NewApp(cfg *config.Config, db *gorm.DB, baseLogger *zerolog.Logger, quoteResponseCache *responsecache.Cache) *App {
	if baseLogger == nil {
		nop := zerolog.Nop()
		baseLogger = &nop
	}

	// Set Gin mode from config or host heuristic.
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

	appLogger := logging.WithComponent(baseLogger, "http_app")

	// Create Gin engine
	router := gin.New()
	router.Use(logging.RequestIDMiddleware(""))
	router.Use(logging.RequestLogger(appLogger))
	router.Use(httpmw.PrometheusHTTP())
	router.Use(gin.Recovery())
	router.Use(httpmw.CORS(cfg.Server.CORS))

	// Create repositories
	quoteRepo := repositories.NewQuoteRepository(db)

	// Create application actions
	getLatestAction := appGetLatest.New(quoteRepo, quoteResponseCache)
	getCountAction := appGetCount.New(quoteRepo)
	getAllAction := appGetAll.New(quoteRepo)
	getByTokenAction := appGetByToken.New(quoteRepo, quoteResponseCache)

	// Create HTTP handlers
	getLatestHandler := httpGetLatest.New(getLatestAction)
	getCountHandler := httpGetCount.New(getCountAction)
	getAllHandler := httpGetAll.New(getAllAction)
	getByTokenHandler := httpGetByToken.New(getByTokenAction)

	// Register /metrics before SetupRoutes so GET /:token does not capture "metrics".
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	httpRouter := NewRouter(RouterConfig{
		GetLatestHandler:  getLatestHandler,
		GetCountHandler:   getCountHandler,
		GetAllHandler:     getAllHandler,
		GetByTokenHandler: getByTokenHandler,
	})
	httpRouter.SetupRoutes(router)

	addr := cfg.Server.Host + ":" + cfg.Server.Port
	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.D(),
		ReadTimeout:       cfg.Server.ReadTimeout.D(),
		WriteTimeout:      cfg.Server.WriteTimeout.D(),
		IdleTimeout:       cfg.Server.IdleTimeout.D(),
	}

	return &App{
		config: cfg,
		db:     db,
		server: server,
		logger: appLogger,
	}
}

func (a *App) Run() error {
	a.logger.Info().Str("addr", a.server.Addr).Msg("starting_http_server")
	return a.server.ListenAndServe()
}

// Server returns the underlying HTTP server for graceful shutdown.
func (a *App) Server() *http.Server {
	return a.server
}
