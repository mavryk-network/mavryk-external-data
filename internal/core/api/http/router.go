package http

import (
	"quotes/internal/core/api/http/quotes/get_all"
	"quotes/internal/core/api/http/quotes/get_count"
	"quotes/internal/core/api/http/quotes/get_latest"

	"github.com/gin-gonic/gin"
)

type Router struct {
	getLatestHandler *get_latest.Handler
	getCountHandler  *get_count.Handler
	getAllHandler    *get_all.Handler
}

func NewRouter(
	getLatestHandler *get_latest.Handler,
	getCountHandler *get_count.Handler,
	getAllHandler *get_all.Handler,
) *Router {
	return &Router{
		getLatestHandler: getLatestHandler,
		getCountHandler:  getCountHandler,
		getAllHandler:    getAllHandler,
	}
}

func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Health check endpoint
	engine.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
			"service": "quotes",
		})
	})

	// API v1 routes
	v1 := engine.Group("/api/v1")
	{
		quotes := v1.Group("/quotes")
		{
			quotes.GET("", r.getAllHandler.Handle)        // GET /api/v1/quotes?from=&to=&limit=
			quotes.GET("/last", r.getLatestHandler.Handle) // GET /api/v1/quotes/last
			quotes.GET("/count", r.getCountHandler.Handle) // GET /api/v1/quotes/count
		}
	}
}
