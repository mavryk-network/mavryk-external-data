package http

import (
	"quotes/internal/core/api/http/handlers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RouterDeps holds the per-domain handler bundles plus DB and the readiness gate.
//
// RWAAuth, when non-nil, wraps /v1/rwa/* and /v1/pairs/rwa in the MBIO JWT
// middleware. It is non-nil on the public listener and nil on the internal
// listener — same handler graph, same code path, different exposure surface.
//
// MountDocs, when true, registers /openapi.yaml and /docs (Swagger UI). It is
// true on both listeners: the spec and Swagger UI are exposed on the public
// engine and access is gated at the infrastructure layer (reverse proxy /
// network policy), not in the app.
//
// See app.go for the wiring.
type RouterDeps struct {
	DB            *gorm.DB
	ReadinessGate *handlers.ReadinessGate
	TokenPrice    handlers.TokenPriceDeps
	TokenCharts   handlers.TokenChartDeps
	RWAPrice      handlers.RWAPriceDeps
	RWACharts     handlers.RWAChartDeps
	RWAPairs      handlers.RWAPairsDeps
	RWAOverview   handlers.RWAOverviewDeps
	Change        handlers.ChangeDeps
	LegacyQuotes  handlers.LegacyQuotesDeps
	Ticker        handlers.TickerDeps
	RWAAuth       gin.HandlerFunc
	MountDocs     bool
}

// SetupRoutes registers all routes on the given engine.
//
// Hierarchy (public listener; internal mirrors this minus RWA auth):
//
//	/healthz                 — liveness
//	/readyz                  — readiness (db ping + drain gate)
//	/metrics                 — Prometheus (internal listener only)
//	/openapi.yaml            — embedded OpenAPI 3.0 spec (public; gated at the edge)
//	/docs                    — Swagger UI (public; gated at the edge)
//	/quotes                  — legacy v0.1.0 wide-format MVRK quotes
//	/v1/prices/:token         — list (range or latest)
//	/v1/prices/:token/latest  — transposed snapshot
//	/v1/prices/:token/count   — total row count
//	/v1/prices/:token/change  — price change over 24h/7d/30d (see design doc)
//	/v1/prices/:token/series  — line chart (see ADR-0015)
//	/v1/prices/:token/ohlc    — OHLC candles
//	/v1/prices/:token/ohlcv   — 501 stub until volume ingestion lands
//	/v1/rwa/:symbol           — list (range or latest); symbol = {base}-{quote}
//	/v1/rwa/:symbol/latest    — transposed snapshot
//	/v1/rwa/:symbol/change    — price change over 24h/7d/30d (native quote only in v1)
//	/v1/rwa/:symbol/series    — line chart (see ADR-0015)
//	/v1/rwa/:symbol/ohlc      — OHLC candles
//	/v1/rwa/:symbol/ohlcv     — 501 stub until volume ingestion lands
//	/v1/pairs/rwa             — enabled RWA pair catalog (discovery)
func SetupRoutes(engine *gin.Engine, deps RouterDeps) {
	engine.GET("/healthz", handlers.Liveness())
	engine.GET("/readyz", handlers.Readiness(deps.DB, deps.ReadinessGate))

	// Legacy v0.1.0 wide-format MVRK quotes. Kept alive outside /v1 for
	// downstream services that still call the old route.
	engine.GET("/quotes", deps.LegacyQuotes.LegacyQuotes())

	// Static OpenAPI 3.0 spec + Swagger UI shell. Both bytes are embedded at
	// compile time via docs.OpenAPIYAML / docs.SwaggerUIHTML. Mounted on the
	// public listener; access is restricted at the infrastructure layer rather
	// than in the app.
	if deps.MountDocs {
		engine.GET("/openapi.yaml", handlers.OpenAPISpec())
		engine.GET("/docs", handlers.SwaggerUI())
		engine.GET("/docs/", handlers.SwaggerUI())
	}

	v1 := engine.Group("/v1")
	{
		ft := v1.Group("/prices")
		{
			ft.GET("/:token", deps.TokenPrice.ListByToken())
			ft.GET("/:token/latest", deps.TokenPrice.LatestSnapshot())
			ft.GET("/:token/count", deps.TokenPrice.Count())
			ft.GET("/:token/change", deps.Change.ChangeFT())
			// FA charts (see ADR-0015). /ohlcv is a 501 stub — the URL
			// is registered from day one to freeze the contract.
			ft.GET("/:token/series", deps.TokenCharts.Series())
			ft.GET("/:token/ohlc", deps.TokenCharts.OHLC())
			ft.GET("/:token/ohlcv", handlers.NotImplementedOHLCV())
		}
		rwa := v1.Group("/rwa")
		if deps.RWAAuth != nil {
			rwa.Use(deps.RWAAuth)
		}
		{
			// GET /v1/rwa — market-overview list (latest + 24h change + 1d/15m
			// mini-series per asset). Registered on the group root; does not
			// conflict with /:symbol (a static node vs its param child). It must
			// live inside the /rwa group to inherit the RWAAuth middleware —
			// unlike /v1/pairs/rwa, whose STATIC segment would collide with
			// /:symbol and so lives outside the group.
			rwa.GET("", deps.RWAOverview.List())
			rwa.GET("/:symbol", deps.RWAPrice.ListBySymbol())
			rwa.GET("/:symbol/latest", deps.RWAPrice.LatestBySymbol())
			rwa.GET("/:symbol/change", deps.Change.ChangeRWA())
			// RWA charts (see ADR-0015). /ohlcv is the same 501 stub
			// shared with the FA side — single source of truth.
			rwa.GET("/:symbol/series", deps.RWACharts.Series())
			rwa.GET("/:symbol/ohlc", deps.RWACharts.OHLC())
			rwa.GET("/:symbol/ohlcv", handlers.NotImplementedOHLCV())
		}
		// Per-exchange ticker market data (CoinGecko /coins/{id}/tickers).
		// Open on both listeners — same posture as /v1/prices (FT data, not
		// tenant-scoped). Mounted only when Ticker.Service is wired.
		if deps.Ticker.Service != nil {
			tk := v1.Group("/tickers")
			{
				tk.GET("/:token/latest", deps.Ticker.LatestByToken())
				tk.GET("/:token/distribution", deps.Ticker.Distribution())
			}
		}
		// Discovery group. Lives outside /v1/rwa because gin's radix
		// tree won't accept a static segment next to /rwa/:symbol. Shares
		// the RWA auth posture — catalog is treated as RWA data.
		pairs := v1.Group("/pairs")
		if deps.RWAAuth != nil {
			pairs.Use(deps.RWAAuth)
		}
		{
			pairs.GET("/rwa", deps.RWAPairs.List())
		}
	}
}
