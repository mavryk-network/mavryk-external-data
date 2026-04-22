package http

import (
	"encoding/json"
	"net/http"

	"quotes/internal/core/api/http/common"
	"quotes/internal/core/api/http/health/db_status"
	"quotes/internal/core/api/http/quotes/get_all"
	"quotes/internal/core/api/http/quotes/get_by_token"
	"quotes/internal/core/api/http/quotes/get_count"
	"quotes/internal/core/api/http/quotes/get_latest"
	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"github.com/swaggo/swag"
)

type Router struct {
	getLatestHandler  *get_latest.Handler
	getCountHandler   *get_count.Handler
	getAllHandler     *get_all.Handler
	getByTokenHandler *get_by_token.Handler
	dbStatusHandler   *db_status.Handler
}

// RouterConfig wires HTTP handlers for NewRouter (struct-arg avoids brittle positional parameters).
type RouterConfig struct {
	GetLatestHandler  *get_latest.Handler
	GetCountHandler   *get_count.Handler
	GetAllHandler     *get_all.Handler
	GetByTokenHandler *get_by_token.Handler
	DBStatusHandler   *db_status.Handler
}

func NewRouter(cfg RouterConfig) *Router {
	return &Router{
		getLatestHandler:  cfg.GetLatestHandler,
		getCountHandler:   cfg.GetCountHandler,
		getAllHandler:     cfg.GetAllHandler,
		getByTokenHandler: cfg.GetByTokenHandler,
		dbStatusHandler:   cfg.DBStatusHandler,
	}
}

func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Swagger: serve dynamic spec so "Try it out" uses this request's host/scheme (same as mavryk-wallet-backend).
	// Gin cannot register both "/swagger/*any" and "/swagger/doc.json", so the JSON lives at /swagger.json.
	//
	// This service also registers GET /:token at root for quotes — a single segment like "swagger" would
	// otherwise be handled as a token name. Explicit /swagger routes must come before that catch-all.
	engine.GET("/swagger.json", func(c *gin.Context) {
		docStr, err := swag.ReadDoc()
		if err != nil {
			common.RespondError(c, coreerrors.Internal("Unable to load API documentation", err))
			return
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(docStr), &doc); err != nil {
			common.RespondError(c, coreerrors.Internal("Unable to load API documentation", err))
			return
		}
		doc["host"] = c.Request.Host
		scheme := c.GetHeader("X-Forwarded-Proto")
		if scheme == "" {
			if c.Request.TLS != nil {
				scheme = "https"
			} else {
				scheme = "http"
			}
		}
		doc["schemes"] = []string{scheme}
		out, err := json.Marshal(doc)
		if err != nil {
			common.RespondError(c, coreerrors.Internal("Unable to load API documentation", err))
			return
		}
		c.Data(200, "application/json; charset=utf-8", out)
	})
	engine.GET("/swagger", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/swagger/index.html")
	})
	engine.GET("/swagger/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/swagger/index.html")
	})
	engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.URL("/swagger.json")))

	// HealthCheck godoc
	// @Summary      Health check
	// @Description  Check if the service is running
	// @Tags         health
	// @Accept       json
	// @Produce      json
	// @Success      200  {object}  map[string]string  "Service status"
	// @Router       /health [get]
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"service": "quotes",
		})
	})
	engine.GET("/health/db", r.dbStatusHandler.Handle)

	v1 := engine.Group("/")
	{
		quotes := v1.Group("/quotes")
		{
			quotes.GET("", r.getAllHandler.Handle)
			quotes.GET("/last", r.getLatestHandler.Handle)
			quotes.GET("/count", r.getCountHandler.Handle)
		}

		// Token-specific endpoint: /:token (e.g., /usdt, /quotes)
		v1.GET("/:token", r.getByTokenHandler.Handle)
	}
}
