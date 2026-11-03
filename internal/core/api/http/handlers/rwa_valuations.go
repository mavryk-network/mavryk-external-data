package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"quotes/internal/core/api/http/common"
	app "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/storage/repositories"
)

type RWAValuationReader interface {
	BySymbol(context.Context, string, string) ([]valuation.Snapshot, error)
	ForContract(context.Context, string, string) ([]valuation.Snapshot, error)
	List(context.Context, string, string, int) ([]valuation.Snapshot, error)
	Attempts(context.Context, string) ([]repositories.ValuationAttempt, error)
	Series(context.Context, valuation.SupplyIdentity, string, app.Interval, time.Time, time.Time, int) ([]repositories.ValuationBucket, repositories.ValuationCoverage, error)
}

type RWAValuationDeps struct {
	Store    RWAValuationReader
	Price    RWAPriceDeps
	Network  string
	MaxLimit int
	Now      func() time.Time
}

func (d RWAValuationDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

type valuationRequest struct {
	Symbol   string
	In       []prices.Currency
	Query    common.PriceQuery
	Interval app.Interval
	After    string
}
type valuationCursor struct {
	Network string `json:"network"`
	After   string `json:"after"`
}
type valuationBatch struct {
	SchemaVersion int                             `json:"schema_version"`
	Items         []valuation.Snapshot            `json:"items"`
	NextCursor    string                          `json:"next_cursor,omitempty"`
	Errors        []repositories.ValuationAttempt `json:"errors"`
}

func (d RWAValuationDeps) bind(c *gin.Context, chart, batch bool) (valuationRequest, error) {
	var req valuationRequest
	if d.Store == nil {
		return req, coreerrors.Unavailable("RWA valuation storage unavailable")
	}
	if !batch {
		base, quote, ok := parseRWASymbol(c.Param("symbol"))
		if !ok || len(c.Param("symbol")) > maxSymbolLen {
			return req, coreerrors.InvalidArgument("symbol must be {base}-{quote}")
		}
		req.Symbol = base + "-" + quote
	}
	var err error
	if chart {
		req.Interval, err = ParseChartInterval(c)
		if err != nil {
			return req, err
		}
		if req.Interval == app.IntervalRaw {
			return req, coreerrors.InvalidArgument("raw is not supported for market cap")
		}
		req.In, err = ParseChartIn(c, d.Price.Converter)
	} else {
		req.In, err = d.Price.parseInQuery(c)
	}
	if err != nil {
		return req, err
	}
	if chart || batch {
		max := d.MaxLimit
		if max <= 0 {
			max = 10000
		}
		if batch {
			max = 100
		}
		req.Query, err = common.BindPriceQuery(c, common.QueryOptions{MaxLimit: max, DefaultLatestLimit: 100})
		if err != nil {
			return req, err
		}
	}
	if chart {
		if req.Query.To.IsZero() {
			req.Query.To = d.now()
		}
		if req.Query.From.IsZero() {
			width, _ := app.BucketDuration(req.Interval)
			req.Query.From = req.Query.To.Add(-time.Duration(req.Query.Limit) * width)
		}
		if err := app.ValidateRange(app.DefaultCaps(), req.Interval, req.Query.From, req.Query.To); err != nil {
			return req, err
		}
	}
	if batch && c.Query("cursor") != "" {
		raw := c.Query("cursor")
		if len(raw) > 1024 {
			return req, coreerrors.InvalidArgument("invalid cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(raw)
		var cursor valuationCursor
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Network != d.Network || cursor.After == "" {
			return req, coreerrors.InvalidArgument("invalid cursor")
		}
		req.After = cursor.After
	}
	return req, nil
}

func (d RWAValuationDeps) resolve(ctx context.Context, c *gin.Context, symbol string) (valuation.Snapshot, bool, error) {
	rows, err := d.Store.BySymbol(ctx, d.Network, symbol)
	if err != nil {
		return valuation.Snapshot{}, false, err
	}
	if len(rows) > 1 {
		return valuation.Snapshot{}, false, coreerrors.Conflict("Ambiguous asset symbol", nil)
	}
	if len(rows) == 1 {
		s := rows[0]
		points := []valuation.Point{}
		for _, p := range s.Valuations {
			if p.Symbol == symbol {
				points = append(points, p)
			}
		}
		if len(points) > 1 {
			return s, false, coreerrors.Conflict("Ambiguous valuation quote", nil)
		}
		s.Valuations = points
		return s, true, nil
	}
	// A known asset with no observation is not an unknown route/asset. Primary
	// resolves through the same catalog as legacy latest, without an orderbook.
	target, err := d.Price.resolveTargetFromPath(c)
	if err != nil {
		return valuation.Snapshot{}, false, err
	}
	contract := target.Pair.TokenAddr
	if !target.HasPair && target.Launch != nil {
		contract = target.Launch.TokenAddr
	}
	if contract != "" {
		rows, err := d.Store.ForContract(ctx, d.Network, contract)
		if err != nil {
			return valuation.Snapshot{}, false, err
		}
		if len(rows) > 1 {
			return valuation.Snapshot{}, false, coreerrors.Conflict("Ambiguous asset identity", nil)
		}
		if len(rows) == 1 {
			s := rows[0]
			s.Valuations = []valuation.Point{}
			if s.Reason == "" {
				s.Reason = "price_missing"
			}
			return s, true, nil
		}
	}
	return valuation.Snapshot{}, false, nil
}

func selectConversions(s valuation.Snapshot, in []prices.Currency) valuation.Snapshot {
	s.Valuations = append([]valuation.Point(nil), s.Valuations...)
	for i := range s.Valuations {
		p := &s.Valuations[i]
		out := map[string]valuation.Conversion{}
		for _, cur := range in {
			v, ok := p.Conversions[string(cur)]
			if !ok {
				v.MarketCapQuality = valuation.Missing("fx_unavailable")
				v.FDVQuality = v.MarketCapQuality
			}
			out[string(cur)] = v
		}
		p.Conversions = out
	}
	return s
}

func (d RWAValuationDeps) Latest() gin.HandlerFunc {
	type request struct {
		valuationRequest
		Snapshot valuation.Snapshot
		Found    bool
	}
	return common.Wrap(func(c *gin.Context) (request, error) {
		r, err := d.bind(c, false, false)
		if err != nil {
			return request{}, err
		}
		s, found, err := d.resolve(c.Request.Context(), c, r.Symbol)
		return request{valuationRequest: r, Snapshot: s, Found: found}, err
	}, func(_ context.Context, req request) (any, error) {
		if !req.Found {
			return map[string]any{"schema_version": 1, "symbol": req.Symbol, "status": "unavailable", "reason": "not_observed", "identity": nil, "revision": nil, "supply": nil, "valuations": []valuation.Point{}}, nil
		}
		return selectConversions(req.Snapshot.At(d.now()), req.In), nil
	})
}

func (d RWAValuationDeps) Batch() gin.HandlerFunc {
	return common.Wrap(func(c *gin.Context) (valuationRequest, error) { return d.bind(c, false, true) }, func(ctx context.Context, req valuationRequest) (valuationBatch, error) {
		out := valuationBatch{SchemaVersion: 1, Items: []valuation.Snapshot{}, Errors: []repositories.ValuationAttempt{}}
		rows, err := d.Store.List(ctx, d.Network, req.After, req.Query.Limit+1)
		if err != nil {
			return out, err
		}
		if len(rows) > req.Query.Limit {
			rows = rows[:req.Query.Limit]
			last := rows[len(rows)-1]
			b, _ := json.Marshal(valuationCursor{Network: d.Network, After: last.Identity.Contract + ":" + last.Identity.TokenID})
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		}
		for _, s := range rows {
			out.Items = append(out.Items, selectConversions(s.At(d.now()), req.In))
		}
		out.Errors, err = d.Store.Attempts(ctx, d.Network)
		if out.Errors == nil {
			out.Errors = []repositories.ValuationAttempt{}
		}
		return out, err
	})
}

func (d RWAValuationDeps) Series() gin.HandlerFunc {
	type request struct {
		valuationRequest
		Snapshot valuation.Snapshot
		Found    bool
	}
	return common.Wrap(func(c *gin.Context) (request, error) {
		r, err := d.bind(c, true, false)
		if err != nil {
			return request{}, err
		}
		s, found, err := d.resolve(c.Request.Context(), c, r.Symbol)
		return request{valuationRequest: r, Snapshot: s, Found: found}, err
	}, func(ctx context.Context, req request) (SeriesDTO, error) {
		out := SeriesDTO{ChartEnvelope: ChartEnvelope{Symbol: req.Symbol, Kind: "series", Interval: string(req.Interval)}, Metric: "market_cap", Points: []SeriesPointDTO{}, Coverage: &SeriesCoverage{From: req.Query.From, To: req.Query.To}}
		if !req.Found {
			out.Gaps = []SeriesGap{{From: req.Query.From, To: req.Query.To, Reason: "not_observed"}}
			return out, nil
		}
		id := req.Snapshot.Identity
		out.Identity = &id
		rows, coverage, err := d.Store.Series(ctx, id, req.Symbol, req.Interval, req.Query.From, req.Query.To, req.Query.Limit)
		if err != nil {
			return out, err
		}
		out.AvailableFrom = coverage.AvailableFrom
		width, _ := app.BucketDuration(req.Interval)
		next := req.Query.From
		for _, row := range rows {
			if row.T.After(next) {
				out.Gaps = append(out.Gaps, SeriesGap{From: next, To: row.T, Reason: "not_observed"})
			}
			p := row.Point
			out.Coverage.Through = &p.AsOf
			end := row.T.Add(width)
			if end.After(req.Query.To) {
				end = req.Query.To
			}
			start := row.T
			if start.Before(req.Query.From) {
				start = req.Query.From
			}
			q := p.MarketCapQuality
			if row.Invalidated {
				q = valuation.Missing("chain_reorg")
			}
			dto := SeriesPointDTO{T: row.T.UTC().Format(time.RFC3339), AsOf: &p.AsOf, Status: q.Status, Reason: q.Reason, PriceBasis: p.PriceBasis, Conv: map[string]num6{}}
			if p.MarketCap != nil && q.Status != "unavailable" {
				dto.P = seriesNumber(*p.MarketCap)
			}
			if len(req.In) > 0 {
				dto.ConversionQuality = map[string]valuation.Quality{}
				dto.FX = map[string]SeriesFX{}
			}
			for _, cur := range req.In {
				key := string(cur)
				v, ok := p.Conversions[key]
				cq := v.MarketCapQuality
				if !ok || v.Rate == nil || v.FXAt == nil {
					cq = valuation.Missing("fx_unavailable")
				}
				if row.Invalidated {
					cq = valuation.Missing("chain_reorg")
				}
				dto.ConversionQuality[key] = cq
				if cq.Status == "unavailable" {
					out.Gaps = append(out.Gaps, SeriesGap{From: start, To: end, Reason: key + ":" + cq.Reason})
				}
				if v.MarketCap != nil && cq.Status != "unavailable" {
					dto.Conv[key] = newNum6(*v.MarketCap)
					dto.FX[key] = SeriesFX{Rate: newNum6(*v.Rate), At: *v.FXAt}
				}
			}
			out.Points = append(out.Points, dto)
			next = end
			if q.Status == "unavailable" {
				out.Gaps = append(out.Gaps, SeriesGap{From: start, To: next, Reason: q.Reason})
			}
		}
		if next.Before(req.Query.To) {
			out.Gaps = append(out.Gaps, SeriesGap{From: next, To: req.Query.To, Reason: "not_observed"})
		}
		out.Coverage.Complete = len(out.Gaps) == 0
		return out, nil
	})
}
