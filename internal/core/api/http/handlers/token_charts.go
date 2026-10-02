package handlers

import (
	"context"
	"strings"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
)

// TokenChartDeps wires the FA chart handlers (Series, OHLC). /ohlcv is bound to
// NotImplementedOHLCV at the router until volume ingestion ships (ADR-0015).
type TokenChartDeps struct {
	Charts        *apiprices.ChartService
	DefaultSource prices.Source
	MaxLimit      int
	DefaultLimit  int
}

// chartTokenRequest is the shared parsed payload for FA chart handlers. Symbol
// and Currency are stamped into the response envelope for the client.
type chartTokenRequest struct {
	Symbol   string
	Currency string
	Query    apiprices.CandleQuery
}

// Series — GET /v1/prices/:token/series
//
// One (timestamp, close) point per bucket; an empty range means latest mode.
func (d TokenChartDeps) Series() gin.HandlerFunc {
	bind := d.bindChart()
	action := func(ctx context.Context, req chartTokenRequest) (SeriesDTO, error) {
		s, err := d.Charts.Series(ctx, req.Query, nil)
		if err != nil {
			return SeriesDTO{}, err
		}
		out := SeriesDTO{
			ChartEnvelope: ChartEnvelope{
				Symbol:   req.Symbol,
				Currency: req.Currency,
				Kind:     "series",
				Interval: string(req.Query.Interval),
			},
			Points: make([]SeriesPointDTO, len(s.Points)),
		}
		for i, p := range s.Points {
			out.Points[i] = SeriesPointDTO{
				T:    p.T.UTC().Format("2006-01-02T15:04:05Z"),
				P:    newNum6(p.P),
				Conv: renderSeriesConv(p.Conv),
			}
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// OHLC — GET /v1/prices/:token/ohlc
//
// One candle per bucket; volume fields belong to OHLCV, parked at 501 (ADR-0015).
func (d TokenChartDeps) OHLC() gin.HandlerFunc {
	bind := d.bindChart()
	action := func(ctx context.Context, req chartTokenRequest) (OHLCDTO, error) {
		oc, err := d.Charts.OHLC(ctx, req.Query, nil)
		if err != nil {
			return OHLCDTO{}, err
		}
		out := OHLCDTO{
			ChartEnvelope: ChartEnvelope{
				Symbol:   req.Symbol,
				Currency: req.Currency,
				Kind:     "ohlc",
				Interval: string(req.Query.Interval),
			},
			Candles: make([]CandleDTO, len(oc.Candles)),
		}
		for i, c := range oc.Candles {
			out.Candles[i] = CandleDTO{
				T:    c.Bucket.UTC().Format("2006-01-02T15:04:05Z"),
				O:    newNum6(c.Open),
				H:    newNum6(c.High),
				L:    newNum6(c.Low),
				C:    newNum6(c.Close),
				N:    c.Samples,
				Conv: renderCandleConv(c.Conv),
			}
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// bindChart parses the path/query params shared by Series and OHLC.
func (d TokenChartDeps) bindChart() common.Bind[chartTokenRequest] {
	return func(c *gin.Context) (chartTokenRequest, error) {
		token := strings.TrimSpace(c.Param("token"))
		if token == "" {
			return chartTokenRequest{}, coreerrors.InvalidArgument("Token name is required")
		}
		t, err := prices.NewToken(token)
		if err != nil {
			return chartTokenRequest{}, prices.ErrTokenNotFound
		}

		interval, err := ParseChartInterval(c)
		if err != nil {
			return chartTokenRequest{}, err
		}
		// interval=raw is reserved for a future stage.
		if interval == apiprices.IntervalRaw {
			return chartTokenRequest{}, coreerrors.InvalidArgument(
				"Interval 'raw' is not yet supported for FA charts; use 1m/5m/15m/1h/4h/1d")
		}

		curRaw := strings.TrimSpace(c.Query("currency"))
		if curRaw == "" {
			return chartTokenRequest{}, coreerrors.InvalidArgument(
				"'currency' query parameter is required (e.g. ?currency=usd)")
		}
		cur, err := prices.NewCurrency(curRaw)
		if err != nil {
			return chartTokenRequest{}, coreerrors.InvalidArgument(
				"Invalid 'currency' parameter: " + curRaw)
		}

		pq, err := common.BindPriceQuery(c, common.QueryOptions{
			MaxLimit:           d.MaxLimit,
			DefaultLatestLimit: d.DefaultLimit,
			// MetricParam: "" — chart endpoints use ?currency= directly.
		})
		if err != nil {
			return chartTokenRequest{}, err
		}

		return chartTokenRequest{
			Symbol:   string(t),
			Currency: string(cur),
			Query: apiprices.CandleQuery{
				EntityKey: string(t),
				AuxKey:    string(d.DefaultSource) + "|" + string(cur),
				From:      pq.From,
				To:        pq.To,
				Interval:  interval,
				Limit:     pq.Limit,
			},
		}, nil
	}
}
