package http

import (
	"encoding/json"

	"quotes/internal/core/api/http/health/db_status"
	"quotes/internal/core/api/http/quotes/get_all"
	"quotes/internal/core/api/http/quotes/get_by_token"
	"quotes/internal/core/api/http/quotes/get_count"
	"quotes/internal/core/api/http/quotes/get_latest"

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

func NewRouter(
	getLatestHandler *get_latest.Handler,
	getCountHandler *get_count.Handler,
	getAllHandler *get_all.Handler,
	getByTokenHandler *get_by_token.Handler,
	dbStatusHandler *db_status.Handler,
) *Router {
	return &Router{
		getLatestHandler:  getLatestHandler,
		getCountHandler:   getCountHandler,
		getAllHandler:     getAllHandler,
		getByTokenHandler: getByTokenHandler,
		dbStatusHandler:   dbStatusHandler,
	}
}

func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Swagger: serve dynamic spec so "Try it out" uses this request's host/scheme (same as mavryk-wallet-backend).
	// Gin cannot register both "/swagger/*any" and "/swagger/doc.json", so the JSON lives at /swagger.json.
	engine.GET("/swagger.json", func(c *gin.Context) {
		docStr, err := swag.ReadDoc()
		if err != nil {
			c.JSON(500, gin.H{"error": "Failed to read swagger doc", "details": err.Error()})
			return
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(docStr), &doc); err != nil {
			c.JSON(500, gin.H{"error": "Failed to parse swagger doc", "details": err.Error()})
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
			c.JSON(500, gin.H{"error": "Failed to serialize swagger doc", "details": err.Error()})
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
