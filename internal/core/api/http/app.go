package http

import (
	"log"
	"quotes/internal/config"
	httpGetAll "quotes/internal/core/api/http/quotes/get_all"
	httpGetCount "quotes/internal/core/api/http/quotes/get_count"
	httpGetLatest "quotes/internal/core/api/http/quotes/get_latest"
	appGetAll "quotes/internal/core/application/quotes/get_all"
	appGetCount "quotes/internal/core/application/quotes/get_count"
	appGetLatest "quotes/internal/core/application/quotes/get_latest"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type App struct {
	config *config.Config
	db     *gorm.DB
	router *gin.Engine
}

func NewApp(cfg *config.Config, db *gorm.DB) *App {
	// Set Gin mode
	if cfg.Server.Host == "localhost" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create Gin engine
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	// Add CORS middleware
	router.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Create repositories
	quoteRepo := repositories.NewQuoteRepository(db)

	// Create application actions
	getLatestAction := appGetLatest.New(quoteRepo)
	getCountAction := appGetCount.New(quoteRepo)
	getAllAction := appGetAll.New(quoteRepo)

	// Create HTTP handlers
	getLatestHandler := httpGetLatest.New(getLatestAction)
	getCountHandler := httpGetCount.New(getCountAction)
	getAllHandler := httpGetAll.New(getAllAction)

	// Create router
	httpRouter := NewRouter(getLatestHandler, getCountHandler, getAllHandler)
	httpRouter.SetupRoutes(router)

	return &App{
		config: cfg,
		db:     db,
		router: router,
	}
}

func (a *App) Run() error {
	addr := a.config.Server.Host + ":" + a.config.Server.Port
	log.Printf("Starting HTTP server on %s", addr)
	return a.router.Run(addr)
}
