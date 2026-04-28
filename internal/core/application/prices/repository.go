// Package prices is the application layer for the price domain. It orchestrates
// the storage repositories and adds optional cross-cutting concerns (caching,
// future rate-limit per consumer, future audit trails).
package prices

import (
	"context"

	"quotes/internal/core/domain/prices"
)

// Repository is the application-facing read/write contract over a price hypertable.
// Both TokenPriceRepository and RWAPriceRepository satisfy this interface.
type Repository interface {
	Save(ctx context.Context, points []prices.PricePoint) (int64, error)
	Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error)
}

// QueryService is the read-only contract used by HTTP handlers; trims Save off
// the surface so handlers can't accidentally write through their dependency.
type QueryService interface {
	Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error)
}
