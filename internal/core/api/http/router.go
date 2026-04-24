package http

import (
	"encoding/json"

	"quotes/internal/core/api/http/common"
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
}

// RouterConfig wires HTTP handlers for NewRouter (struct-arg avoids brittle positional parameters).
type RouterConfig struct {
	GetLatestHandler  *get_latest.Handler
	GetCountHandler   *get_count.Handler
	GetAllHandler     *get_all.Handler
	GetByTokenHandler *get_by_token.Handler
}

func NewRouter(cfg RouterConfig) *Router {
	return &Router{
		getLatestHandler:  cfg.GetLatestHandler,
		getCountHandler:   cfg.GetCountHandler,
		getAllHandler:     cfg.GetAllHandler,
		getByTokenHandler: cfg.GetByTokenHandler,
	}
}

func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Swagger documentation
	// Serve a dynamic doc.json so Swagger UI always targets the same host/scheme it was loaded from
	// (prevents "Try it out" from using stale hardcoded hosts in prod).
	//
	// Note: Gin cannot register both "/swagger/*any" and "/swagger/doc.json" (conflicting routes),
	// so we expose the doc at a separate top-level path.
	//
	// Register these before GET /:token at root so "swagger" is not treated as a token name.
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
