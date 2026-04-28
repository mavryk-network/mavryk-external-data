package http

import (
	"quotes/internal/core/api/http/handlers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RouterDeps holds the per-domain handler bundles plus DB and the readiness gate.
type RouterDeps struct {
	DB            *gorm.DB
	ReadinessGate *handlers.ReadinessGate
	TokenPrice    handlers.TokenPriceDeps
	RWAPrice      handlers.RWAPriceDeps
}

// SetupRoutes registers all routes on the given engine.
//
// Hierarchy:
//
//	/healthz                 — liveness
//	/readyz                  — readiness (db ping + drain gate)
//	/metrics                 — Prometheus
//	/openapi.yaml            — embedded OpenAPI 3.0 spec
//	/docs                    — Swagger UI loading /openapi.yaml
//	/v1/prices/:token        — list (range or latest)
//	/v1/prices/:token/latest — transposed snapshot
//	/v1/prices/:token/count  — total row count
//	/v1/rwa/:pair_id         — list (range or latest)
//	/v1/rwa/:pair_id/latest  — transposed snapshot
func SetupRoutes(engine *gin.Engine, deps RouterDeps) {
	engine.GET("/healthz", handlers.Liveness())
	engine.GET("/readyz", handlers.Readiness(deps.DB, deps.ReadinessGate))

	// Static OpenAPI 3.0 spec + Swagger UI shell. Both bytes are embedded at
	// compile time via docs.OpenAPIYAML / docs.SwaggerUIHTML.
	engine.GET("/openapi.yaml", handlers.OpenAPISpec())
	engine.GET("/docs", handlers.SwaggerUI())
	engine.GET("/docs/", handlers.SwaggerUI())

	v1 := engine.Group("/v1")
	{
		ft := v1.Group("/prices")
		{
			ft.GET("/:token", deps.TokenPrice.ListByToken())
			ft.GET("/:token/latest", deps.TokenPrice.LatestSnapshot())
			ft.GET("/:token/count", deps.TokenPrice.Count())
		}
		rwa := v1.Group("/rwa")
		{
			rwa.GET("/:pair_id", deps.RWAPrice.ListByPair())
			rwa.GET("/:pair_id/latest", deps.RWAPrice.LatestByPair())
		}
	}
}
