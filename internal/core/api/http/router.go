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
	TokenCharts   handlers.TokenChartDeps
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
//	/v1/prices/:token         — list (range or latest)
//	/v1/prices/:token/latest  — transposed snapshot
//	/v1/prices/:token/count   — total row count
//	/v1/prices/:token/series  — line chart (Stage 1, charts.md)
//	/v1/prices/:token/ohlc    — OHLC candles (Stage 1)
//	/v1/prices/:token/ohlcv   — 501 stub until Stage 4 (charts.md §1.1)
//	/v1/rwa/:symbol           — list (range or latest); symbol = {base}-{quote}
//	/v1/rwa/:symbol/latest    — transposed snapshot
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
			// Charts (Stage 1, charts.md). /ohlcv is a 501 stub — the URL
			// is registered from day one to freeze the contract.
			ft.GET("/:token/series", deps.TokenCharts.Series())
			ft.GET("/:token/ohlc", deps.TokenCharts.OHLC())
			ft.GET("/:token/ohlcv", handlers.NotImplementedOHLCV())
		}
		rwa := v1.Group("/rwa")
		{
			rwa.GET("/:symbol", deps.RWAPrice.ListBySymbol())
			rwa.GET("/:symbol/latest", deps.RWAPrice.LatestBySymbol())
		}
	}
}
