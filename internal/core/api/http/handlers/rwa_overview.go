package handlers

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	"quotes/internal/core/application/cache"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// Mini-chart shape for the overview list: last 24h at 15m buckets (~96 close
// points). Fixed (not query-parameterised) to keep the list payload bounded.
const (
	overviewSeriesInterval   = apiprices.Interval15m
	overviewSeriesWindow     = 24 * time.Hour
	defaultOverviewMaxAssets = 200
	// overviewConcurrency bounds the per-asset query fan-out. Each asset does one
	// (cached) change call + one series call; a small pool keeps single-request
	// latency low without swamping the DB pool when many assets are enabled.
	overviewConcurrency = 8
)

// RWAOverviewDeps wires GET /v1/rwa — the market-overview list of enabled RWA
// assets. Each asset carries its latest price, 24h change, and a short
// (24h / 15m) close-price series.
//
// It composes the SAME service instances the per-symbol endpoints use: the RWA
// ChangeService (which returns `now` + the 24h anchor in one cached, singleflight
// call) and the RWA ChartService (the series). Sharing the instances means the
// overview shares their caches with /v1/rwa/:symbol/change and /series.
type RWAOverviewDeps struct {
	Pairs     RWAPairsLister           // enabled asset catalog (EnabledRWAPairs)
	Change    *apiprices.ChangeService // required: latest (`now`) + 24h change
	Charts    *apiprices.ChartService  // required: 24h/15m close series
	Converter apiprices.PriceConverter // optional; enables `?in=` (single currency)
	Source    prices.Source            // typically prices.SourceEquiteez
	// MaxAssets caps how many assets a single response may contain (defends the
	// per-asset query fan-out). 0 falls back to defaultOverviewMaxAssets.
	MaxAssets int

	// cache is an optional short-TTL response cache (see NewRWAOverviewDeps).
	// nil / disabled means every request assembles fresh. Keyed by (limit, in)
	// so distinct query shapes don't collide.
	cache *cache.TTL[rwaOverviewDTO]
}

// NewRWAOverviewDeps wires the overview handler with an optional short-TTL
// response cache (cacheTTL <= 0 disables it). The cache amortises the per-asset
// query fan-out across the near-identical requests a polling dashboard produces;
// the underlying ChangeService cache still bounds staleness within one build.
func NewRWAOverviewDeps(
	pairs RWAPairsLister,
	change *apiprices.ChangeService,
	charts *apiprices.ChartService,
	converter apiprices.PriceConverter,
	source prices.Source,
	cacheTTL time.Duration,
) RWAOverviewDeps {
	return RWAOverviewDeps{
		Pairs:     pairs,
		Change:    change,
		Charts:    charts,
		Converter: converter,
		Source:    source,
		cache:     cache.New[rwaOverviewDTO](cacheTTL, nil),
	}
}

func (d RWAOverviewDeps) maxAssets() int {
	if d.MaxAssets > 0 {
		return d.MaxAssets
	}
	return defaultOverviewMaxAssets
}

// List — GET /v1/rwa
//
// Returns the list of enabled RWA assets with dynamic data. Empty catalog
// returns `{ "assets": [] }`, not 404. Each asset carries its own `price_as_of`;
// `token_address` is the on-chain RWA token contract (null when not synced yet).
//
// Query params:
//   - limit (optional) — cap the number of assets; clamped to the server cap.
//   - in    (optional) — single FX target; converts the latest price (flat
//     top-level key per asset) and every mini-series point. >1 currency → 400,
//     mirroring the chart endpoints (the mini-series supports one target).
func (d RWAOverviewDeps) List() gin.HandlerFunc {
	type request struct {
		Limit int
		In    []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		limit := d.maxAssets()
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n <= 0 {
				return request{}, coreerrors.InvalidArgument("Invalid 'limit' parameter: must be a positive integer")
			}
			if n < limit {
				limit = n
			}
		}
		in, err := ParseChartIn(c, d.Converter) // caps to <=1 currency
		if err != nil {
			return request{}, err
		}
		return request{Limit: limit, In: in}, nil
	}
	action := func(ctx context.Context, req request) (rwaOverviewDTO, error) {
		if d.cache != nil && d.cache.Enabled() {
			return d.cache.GetOrLoad(ctx, overviewCacheKey(req.Limit, req.In), func(ctx context.Context) (rwaOverviewDTO, error) {
				return d.assemble(ctx, req.Limit, req.In)
			})
		}
		return d.assemble(ctx, req.Limit, req.In)
	}
	return common.Wrap(bind, action)
}

// assemble fetches the enabled asset catalog and builds every asset's dynamic
// block concurrently (bounded pool), then composes the response. First per-asset
// error cancels the rest and fails the request.
func (d RWAOverviewDeps) assemble(ctx context.Context, limit int, in []prices.Currency) (rwaOverviewDTO, error) {
	pairs, err := d.Pairs.EnabledRWAPairs(ctx)
	if err != nil {
		return rwaOverviewDTO{}, err
	}
	// Only this source, stable order (deterministic response + snapshot tests).
	filtered := make([]prices.RWAPair, 0, len(pairs))
	for _, p := range pairs {
		if p.Source == d.Source {
			filtered = append(filtered, p)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return overviewSymbol(filtered[i]) < overviewSymbol(filtered[j]) })
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	now := time.Now()
	assets := make([]rwaOverviewAsset, len(filtered))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(overviewConcurrency)
	for i := range filtered {
		i, pair := i, filtered[i]
		g.Go(func() error {
			asset, err := d.buildAsset(gctx, pair, now, in)
			if err != nil {
				return err
			}
			assets[i] = asset
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return rwaOverviewDTO{}, err
	}
	return rwaOverviewDTO{Assets: assets}, nil
}

// overviewCacheKey partitions cached responses by the params that shape them:
// the asset cap and the (≤1) FX target currency.
func overviewCacheKey(limit int, in []prices.Currency) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(limit))
	b.WriteByte('|')
	for i, c := range in {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(c))
	}
	return b.String()
}

// buildAsset assembles one asset's dynamic block. Missing data renders as
// nulls / empty series — the asset still appears in the list.
func (d RWAOverviewDeps) buildAsset(ctx context.Context, pair prices.RWAPair, now time.Time, in []prices.Currency) (rwaOverviewAsset, error) {
	nativeQuote := strings.ToLower(pair.QuoteSymbol)
	asset := rwaOverviewAsset{
		Symbol:       overviewSymbol(pair),
		Base:         strings.ToLower(pair.BaseSymbol),
		Quote:        nativeQuote,
		NativeQuote:  nativeQuote,
		TokenAddress: nilIfEmpty(pair.TokenAddr),
		Series:       seriesMiniDTO{Interval: string(overviewSeriesInterval), Points: []SeriesPointDTO{}},
	}

	// Latest price + 24h change in one call (cached + singleflight).
	res, err := d.Change.GetChange(ctx, apiprices.ChangeQuery{
		Source:     d.Source,
		EntityKey:  pair.EntityKey(),
		AuxKey:     lastSide,
		Currencies: []string{nativeQuote},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err != nil {
		return rwaOverviewAsset{}, err
	}
	if cur, ok := res.Currencies[nativeQuote]; ok {
		if cur.NowFound {
			p := newNum6(cur.Now)
			ts := formatRFC3339(cur.NowTS)
			asset.Price = &p
			asset.PriceAsOf = &ts
			if len(in) > 0 && d.Converter != nil {
				if quoteToken, resolved := promoteQuoteToken(pair.QuoteSymbol); resolved {
					asset.Converted = convertNowFlat(ctx, d.Converter, quoteToken, in, cur.Now, cur.NowTS)
				}
			}
		}
		if cfp, ok := cur.ByPeriod[prices.Period24h]; ok && cfp.AnchorFound && cfp.ChangePctValid {
			da := newNum6(cfp.DeltaAbs)
			cp := newNum6(cfp.ChangePct)
			asset.Change24h = change24hDTO{DeltaAbs: &da, ChangePct: &cp}
		}
	}

	// Mini series (24h / 15m). Unregistered native quote ⇒ drop `?in=` for the
	// series (no FX source), same silent-drop semantics as the chart endpoint.
	var sourceToken prices.Token
	seriesIn := in
	if t, terr := prices.NewToken(pair.QuoteSymbol); terr == nil {
		sourceToken = t
	} else {
		seriesIn = nil
	}
	s, err := d.Charts.Series(ctx, apiprices.CandleQuery{
		EntityKey:   pair.EntityKey(),
		AuxKey:      lastSide,
		From:        now.Add(-overviewSeriesWindow),
		To:          now,
		Interval:    overviewSeriesInterval,
		SourceToken: sourceToken,
	}, seriesIn)
	if err != nil {
		return rwaOverviewAsset{}, err
	}
	points := make([]SeriesPointDTO, len(s.Points))
	for i, p := range s.Points {
		points[i] = SeriesPointDTO{
			T:    formatRFC3339(p.T),
			P:    newNum6(p.P),
			Conv: renderSeriesConv(p.Conv),
		}
	}
	asset.Series.Points = points

	return asset, nil
}

// nilIfEmpty returns nil for an empty string so JSON renders `null` instead of
// "" (e.g. an RWA pair synced without a token address yet).
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// overviewSymbol renders a pair as the canonical lower-cased `{base}-{quote}`.
func overviewSymbol(p prices.RWAPair) string {
	return strings.ToLower(p.BaseSymbol) + "-" + strings.ToLower(p.QuoteSymbol)
}

// --- DTOs ---

type rwaOverviewDTO struct {
	Assets []rwaOverviewAsset `json:"assets"`
}

// rwaOverviewAsset is one row of the overview. `?in=` conversions of the latest
// price inline as flat top-level numeric keys (3-letter ISO codes never collide
// with the reserved field names), matching /v1/rwa/:symbol/latest.
type rwaOverviewAsset struct {
	Symbol       string
	Base         string
	Quote        string
	NativeQuote  string
	TokenAddress *string // on-chain RWA token contract; null when not synced yet
	Price        *num6   // null until a `last` tick exists
	PriceAsOf    *string
	Change24h    change24hDTO
	Series       seriesMiniDTO
	Converted    map[string]num6 // ?in= per-target latest price; nil when absent
}

func (a rwaOverviewAsset) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 9+len(a.Converted))
	out["symbol"] = a.Symbol
	out["base"] = a.Base
	out["quote"] = a.Quote
	out["native_quote"] = a.NativeQuote
	out["token_address"] = a.TokenAddress
	out["price"] = a.Price
	out["price_as_of"] = a.PriceAsOf
	out["change_24h"] = a.Change24h
	out["series_1d"] = a.Series
	for cur, v := range a.Converted {
		out[cur] = v
	}
	return json.Marshal(out)
}

// change24hDTO is the 24h change block. delta_abs is in the native quote;
// change_pct is unitless. Both null when the 24h anchor is missing or the
// baseline price was zero (Decision #10, mirrors /change).
type change24hDTO struct {
	DeltaAbs  *num6 `json:"delta_abs"`
	ChangePct *num6 `json:"change_pct"`
}

// seriesMiniDTO is the short 1-day chart. Reuses SeriesPointDTO ({t,p}, plus
// flat converted key per point on `?in=`) so clients parse the same point shape
// as /v1/rwa/:symbol/series.
type seriesMiniDTO struct {
	Interval string           `json:"interval"`
	Points   []SeriesPointDTO `json:"points"`
}
