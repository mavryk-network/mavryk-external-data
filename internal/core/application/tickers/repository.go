// Package tickers is the application layer for the ticker domain (per-exchange
// CoinGecko data). Mirrors application/prices in shape: a Repository interface
// over the storage layer plus a thin CachedRepository decorator.
//
// The Repository is intentionally narrow — only the queries the handler
// surfaces, plus the write path used by the job. Adding a new query type means
// a new method here, not a leaky GORM tx exposed to callers.
package tickers

import (
	"context"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"
)

// Repository is the read/write contract used by the HTTP handlers and the
// CoinGecko tickers job. Implementations live under
// internal/core/infrastructure/storage/repositories.
type Repository interface {
	// SaveSnapshot upserts the supplied exchanges into the lookup AND inserts
	// the tickers into the hypertable inside a single transaction. The FK on
	// token_tickers.exchange_id requires exchanges to land first, but the tx
	// boundary ensures atomicity even when CG returns a brand-new exchange.
	//
	// Idempotent on (token, source, exchange, target, ts) — ON CONFLICT
	// DO NOTHING absorbs duplicate ticks within a 5-minute cadence.
	SaveSnapshot(ctx context.Context, exchanges []tickers.Exchange, rows []tickers.Ticker) (int64, error)

	// LatestSnapshot returns one row per (exchange, target) for `token` from
	// `source`, freshest first. When includeStale is false, rows whose
	// `ts < now - staleAfter` are filtered out. Each row carries the 1D
	// change % computed at query time via LATERAL against the same hypertable
	// (see ADR-0013 §Consequences / TICKERS-4 for the escape-hatch CA).
	//
	// Empty result is not an error — handler returns 200 with an empty array.
	LatestSnapshot(ctx context.Context, q LatestQuery) (tickers.Snapshot, error)

	// VolumeDistribution returns one row per group (exchange or target) with
	// the freshest-per-pair `volume_24h_base` summed inside each group. Stale
	// rows (ts < now - staleAfter) are ALWAYS excluded — pie charts are about
	// current market structure, never historical noise.
	//
	// When Total = 0 (cold start, all volumes nil), Rows is empty.
	VolumeDistribution(ctx context.Context, q DistributionQuery) (tickers.Distribution, error)
}

// LatestQuery bundles read params for /v1/tickers/:token/latest.
type LatestQuery struct {
	Token        prices.Token
	Source       prices.Source
	IncludeStale bool
	StaleAfter   time.Duration // 0 disables the stale fence regardless of IncludeStale
}

// DistributionQuery bundles read params for /v1/tickers/:token/distribution.
type DistributionQuery struct {
	Token      prices.Token
	Source     prices.Source
	GroupBy    tickers.GroupBy
	StaleAfter time.Duration // 0 disables the stale fence
}

// QueryService is the read-only contract used by HTTP handlers.
type QueryService interface {
	LatestSnapshot(ctx context.Context, q LatestQuery) (tickers.Snapshot, error)
	VolumeDistribution(ctx context.Context, q DistributionQuery) (tickers.Distribution, error)
}
