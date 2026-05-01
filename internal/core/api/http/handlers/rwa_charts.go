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

// rwaChartLastSide is the only orderbook side surfaced on chart endpoints.
// Mirrors the choice baked into ListBySymbol/LatestBySymbol (rwa_prices.go).
// bid/ask/mid stay in storage for a future spread-analysis endpoint, not on
// the chart contract.
const rwaChartLastSide = string(prices.SideLast)

// RWAChartDeps wires the RWA chart handlers (Series, OHLC).
//
// /ohlcv is intentionally not on this struct — Stage 4 of charts.md hosts
// it; the route binds NotImplementedOHLCV at the router until then.
//
// `Charts` is shared with the token-side charts handler in spirit (same
// ChartService type), but each gets its own instance — one wraps
// TokenPriceRepository, the other RWAPriceRepository. Converter is left
// nil here too; Stage 3 wires the close-of-bucket FX path.
type RWAChartDeps struct {
	Charts        *apiprices.ChartService
	Lookup        PairLookup
	DefaultSource prices.Source
	MaxLimit      int
	DefaultLimit  int
}

// chartRWARequest is the shared parsed-payload for RWA chart handlers.
// `Symbol` keeps the original `{base}-{quote}` URL form so dashboards can
// echo it; `Currency` is the pair's native_quote (e.g. "usdt"). Pair is
// kept for the EntityKey/AuxKey CandleQuery wiring. `In` carries the (≤1)
// FX target from `?in=` — empty when no conversion was requested.
type chartRWARequest struct {
	Symbol   string
	Currency string
	Pair     prices.RWAPair
	Query    apiprices.CandleQuery
	In       []prices.Currency
}

// Series — GET /v1/rwa/:symbol/series
//
// One (timestamp, close) point per bucket on the `last` side. History
// before service deploy depends on `equiteez_backfill.go` having been
// enabled — see charts.md §3.1 ("Backfill") and the operational note in
// the OpenAPI description.
//
// Optional `?in=<currency>` adds a flat top-level numeric key per point
// with the close price converted at the close-of-bucket FX rate
// (charts.md §6). One target only — multiple currencies → 400.
func (d RWAChartDeps) Series() gin.HandlerFunc {
	bind := d.bindChart()
	action := func(ctx context.Context, req chartRWARequest) (SeriesDTO, error) {
		s, err := d.Charts.Series(ctx, req.Query, req.In)
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

// OHLC — GET /v1/rwa/:symbol/ohlc
//
// Candles without volume. Stage 4 (charts.md §1.1) lifts /ohlcv with
// real traded-volume from `orderbook_order`; until then the OHLCV route
// is a 501 stub bound directly at the router.
//
// Optional `?in=<currency>` adds a nested object per candle with
// converted o/h/l/c plus rate/rate_ts (charts.md §3.2). One FX rate per
// candle (close-of-bucket) keeps the candle valid (`l ≤ o,c ≤ h`).
func (d RWAChartDeps) OHLC() gin.HandlerFunc {
	bind := d.bindChart()
	action := func(ctx context.Context, req chartRWARequest) (OHLCDTO, error) {
		oc, err := d.Charts.OHLC(ctx, req.Query, req.In)
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

// bindChart parses the path/query params shared by Series and OHLC. The
// ?in= parser caps to ≤1 currency for charts (charts.md §6). Source token
// for FX is the pair's native quote, promoted to a registered Token —
// unregistered native quotes still return 200 from the chart endpoint
// because in= just gets dropped silently per the legacy /v1/rwa/{symbol}
// semantics.
func (d RWAChartDeps) bindChart() common.Bind[chartRWARequest] {
	return func(c *gin.Context) (chartRWARequest, error) {
		raw := c.Param("symbol")
		base, quote, ok := parseRWASymbol(raw)
		if !ok {
			return chartRWARequest{}, coreerrors.InvalidArgument(
				"symbol must be {base}-{quote}, e.g. mars1-usdt")
		}
		pair, err := d.Lookup.LookupRWAPairBySymbol(c.Request.Context(), base, quote)
		if err != nil {
			return chartRWARequest{}, err
		}

		interval, err := ParseChartInterval(c)
		if err != nil {
			return chartRWARequest{}, err
		}
		// Stage 3 ships line + ohlc + full granularities — interval=raw is
		// still reserved for a future stage that would wire the existing
		// PointRepository.Query path against rwa_quote_prices.
		if interval == apiprices.IntervalRaw {
			return chartRWARequest{}, coreerrors.InvalidArgument(
				"Interval 'raw' is not yet supported for RWA charts; use 1m/5m/15m/1h/4h/1d")
		}

		pq, err := common.BindPriceQuery(c, common.QueryOptions{
			MaxLimit:           d.MaxLimit,
			DefaultLatestLimit: d.DefaultLimit,
		})
		if err != nil {
			return chartRWARequest{}, err
		}

		inTargets, err := ParseChartIn(c, d.Charts.Converter)
		if err != nil {
			return chartRWARequest{}, err
		}

		// SourceToken is the pair's native quote. NewToken validates against
		// the runtime registry — unregistered ones are dropped silently
		// (no FX possible without a known source). The handler still
		// processes the request, just without conversion.
		var sourceToken prices.Token
		if t, terr := prices.NewToken(pair.QuoteSymbol); terr == nil {
			sourceToken = t
		} else if len(inTargets) > 0 {
			// Caller asked for FX but the pair's quote isn't registered —
			// silently drop the in= rather than 500ing. Same wire-side
			// semantics as the legacy `/v1/rwa/{symbol}` endpoint.
			inTargets = nil
		}

		// Symbol on the wire mirrors the URL — preserve case-insensitively
		// the {base}-{quote} the caller sent. Currency is the pair's native
		// quote, lower-cased to match other endpoints.
		symbolOut := strings.ToLower(strings.TrimSpace(raw))

		return chartRWARequest{
			Symbol:   symbolOut,
			Currency: strings.ToLower(pair.QuoteSymbol),
			Pair:     pair,
			In:       inTargets,
			Query: apiprices.CandleQuery{
				EntityKey:   pair.EntityKey(),
				AuxKey:      rwaChartLastSide,
				From:        pq.From,
				To:          pq.To,
				Interval:    interval,
				Limit:       pq.Limit,
				SourceToken: sourceToken,
			},
		}, nil
	}
}
