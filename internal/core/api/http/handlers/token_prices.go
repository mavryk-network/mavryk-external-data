// Package handlers contains all HTTP request handlers. Generic-Wrap based; each
// handler is a thin bind+action composition with no error-handling boilerplate.
package handlers

import (
	"context"
	"strings"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// TokenPriceDeps wires the FT-side dependencies for the handlers below.
type TokenPriceDeps struct {
	Service       apiprices.QueryService
	Repo          *repositories.TokenPriceRepository
	DefaultSource prices.Source // typically prices.SourceCoinGecko
	MaxLimit      int
	DefaultLimit  int
}

// ListByToken — GET /v1/prices/:token
//
// Query params:
//   - from / to (RFC3339)             — time window; both omitted → latest
//   - currency=usd,eur                — optional metric filter
//   - limit                            — capped by MaxLimit
func (d TokenPriceDeps) ListByToken() gin.HandlerFunc {
	type request struct {
		Token  string
		Source prices.Source
		Query  prices.Query
	}
	bind := func(c *gin.Context) (request, error) {
		token := strings.TrimSpace(c.Param("token"))
		if token == "" {
			return request{}, coreerrors.InvalidArgument("Token name is required")
		}
		t, err := prices.NewToken(token)
		if err != nil {
			return request{}, prices.ErrTokenNotFound
		}
		opts := common.QueryOptions{
			MaxLimit:           d.MaxLimit,
			DefaultLatestLimit: d.DefaultLimit,
			MetricParam:        "currency",
		}
		pq, err := common.BindPriceQuery(c, opts)
		if err != nil {
			return request{}, err
		}
		for _, m := range pq.Metrics {
			if _, err := prices.NewCurrency(m); err != nil {
				return request{}, coreerrors.InvalidArgument("Invalid 'currency' value: " + m)
			}
		}
		return request{
			Token:  string(t),
			Source: d.DefaultSource,
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: string(t),
				Metrics:   pq.Metrics,
				From:      pq.From,
				To:        pq.To,
				Limit:     pq.Limit,
			},
		}, nil
	}
	type pointDTO struct {
		Timestamp string          `json:"timestamp"`
		Currency  string          `json:"currency"`
		Price     decimal.Decimal `json:"price"`
	}
	action := func(ctx context.Context, req request) ([]pointDTO, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return nil, err
		}
		out := make([]pointDTO, len(points))
		for i, p := range points {
			out[i] = pointDTO{
				Timestamp: p.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				Currency:  p.Metric,
				Price:     p.Price,
			}
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// LatestSnapshot — GET /v1/prices/:token/latest
//
// Returns the freshest price for each currency, transposed into one Snapshot
// object. Spiritual replacement for the legacy `/quotes/last`.
func (d TokenPriceDeps) LatestSnapshot() gin.HandlerFunc {
	type request struct {
		Query prices.Query
	}
	bind := func(c *gin.Context) (request, error) {
		token := strings.TrimSpace(c.Param("token"))
		if token == "" {
			return request{}, coreerrors.InvalidArgument("Token name is required")
		}
		t, err := prices.NewToken(token)
		if err != nil {
			return request{}, prices.ErrTokenNotFound
		}
		return request{
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: string(t),
			},
		}, nil
	}
	action := func(ctx context.Context, req request) (prices.Snapshot, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return prices.Snapshot{}, err
		}
		snap, ok := prices.LatestSnapshot(points)
		if !ok {
			return prices.Snapshot{}, coreerrors.NotFound("No prices for token")
		}
		return snap, nil
	}
	return common.Wrap(bind, action)
}

// Count — GET /v1/prices/:token/count
//
// Returns the total raw row count in token_prices for (token, source).
func (d TokenPriceDeps) Count() gin.HandlerFunc {
	type request struct {
		Token string
	}
	bind := func(c *gin.Context) (request, error) {
		token := strings.TrimSpace(c.Param("token"))
		if token == "" {
			return request{}, coreerrors.InvalidArgument("Token name is required")
		}
		t, err := prices.NewToken(token)
		if err != nil {
			return request{}, prices.ErrTokenNotFound
		}
		return request{Token: string(t)}, nil
	}
	type response struct {
		Token string `json:"token"`
		Count int64  `json:"count"`
	}
	action := func(ctx context.Context, req request) (response, error) {
		n, err := d.Repo.Count(ctx, d.DefaultSource, req.Token)
		if err != nil {
			return response{}, err
		}
		return response{Token: req.Token, Count: n}, nil
	}
	return common.Wrap(bind, action)
}
