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
	}
}
