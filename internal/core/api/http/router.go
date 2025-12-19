package http

import (
	"quotes/internal/core/api/http/quotes/get_all"
	"quotes/internal/core/api/http/quotes/get_by_token"
	"quotes/internal/core/api/http/quotes/get_count"
	"quotes/internal/core/api/http/quotes/get_latest"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

type Router struct {
	getLatestHandler  *get_latest.Handler
	getCountHandler   *get_count.Handler
	getAllHandler     *get_all.Handler
	getByTokenHandler *get_by_token.Handler
}

func NewRouter(
	getLatestHandler *get_latest.Handler,
	getCountHandler *get_count.Handler,
	getAllHandler *get_all.Handler,
	getByTokenHandler *get_by_token.Handler,
) *Router {
	return &Router{
		getLatestHandler:  getLatestHandler,
		getCountHandler:   getCountHandler,
		getAllHandler:     getAllHandler,
		getByTokenHandler: getByTokenHandler,
	}
}

func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Swagger documentation
	engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

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
