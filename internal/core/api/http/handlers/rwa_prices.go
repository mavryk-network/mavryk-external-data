package handlers

import (
	"context"
	"strconv"
	"strings"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// RWAPriceDeps wires the RWA-side dependencies.
type RWAPriceDeps struct {
	Service       apiprices.QueryService
	DefaultSource prices.Source // typically prices.SourceEquiteez
	MaxLimit      int
	DefaultLimit  int
}

// ListByPair — GET /v1/rwa/:pair_id
func (d RWAPriceDeps) ListByPair() gin.HandlerFunc {
	type request struct {
		PairID int64
		Query  prices.Query
	}
	bind := func(c *gin.Context) (request, error) {
		raw := strings.TrimSpace(c.Param("pair_id"))
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || pid <= 0 {
			return request{}, coreerrors.InvalidArgument("pair_id must be a positive integer")
		}
		opts := common.QueryOptions{
			MaxLimit:           d.MaxLimit,
			DefaultLatestLimit: d.DefaultLimit,
			MetricParam:        "side",
		}
		pq, err := common.BindPriceQuery(c, opts)
		if err != nil {
			return request{}, err
		}
		for _, m := range pq.Metrics {
			if _, err := prices.NewSide(m); err != nil {
				return request{}, coreerrors.InvalidArgument("Invalid 'side' value: " + m)
			}
		}
		return request{
			PairID: pid,
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: strconv.FormatInt(pid, 10),
				Metrics:   pq.Metrics,
				From:      pq.From,
				To:        pq.To,
				Limit:     pq.Limit,
			},
		}, nil
	}
	type pointDTO struct {
		Timestamp string           `json:"timestamp"`
		Side      string           `json:"side"`
		Price     decimal.Decimal  `json:"price"`
		Size      *decimal.Decimal `json:"size,omitempty"`
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
				Side:      p.Metric,
				Price:     p.Price,
				Size:      p.Size,
			}
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// LatestByPair — GET /v1/rwa/:pair_id/latest
func (d RWAPriceDeps) LatestByPair() gin.HandlerFunc {
	type request struct {
		Query prices.Query
	}
	bind := func(c *gin.Context) (request, error) {
		raw := strings.TrimSpace(c.Param("pair_id"))
		pid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || pid <= 0 {
			return request{}, coreerrors.InvalidArgument("pair_id must be a positive integer")
		}
		return request{
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: strconv.FormatInt(pid, 10),
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
			return prices.Snapshot{}, coreerrors.NotFound("No prices for pair")
		}
		return snap, nil
	}
	return common.Wrap(bind, action)
}
