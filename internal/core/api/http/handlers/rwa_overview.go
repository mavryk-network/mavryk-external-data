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

// Mini-chart shape: last 24h at 15m buckets, fixed rather than
// query-parameterised so the list payload stays bounded.
const (
	overviewSeriesInterval   = apiprices.Interval15m
	overviewSeriesWindow     = 24 * time.Hour
	defaultOverviewMaxAssets = 200
	// overviewConcurrency bounds the per-asset query fan-out.
	overviewConcurrency = 8

	// market tells a client which block to expect: a live orderbook quote or a
	// fixed launchpad sale price.
	marketSecondary = "secondary"
	marketPrimary   = "primary"
)

// RWAOverviewDeps wires GET /v1/rwa — the market-overview list of enabled RWA
// assets. It composes the SAME ChangeService and ChartService instances the
// per-symbol endpoints use, so the overview shares their caches.
type RWAOverviewDeps struct {
	Pairs     RWAPairsLister           // enabled asset catalog (EnabledRWAPairs)
	Launches  RWALaunchLister          // optional: primary-issuance assets (no orderbook)
	Change    *apiprices.ChangeService // required: latest (`now`) + 24h change
	Charts    *apiprices.ChartService  // required: 24h/15m close series
	Converter apiprices.PriceConverter // optional; enables `?in=` (single currency)
	Source    prices.Source            // typically prices.SourceEquiteez
	// MaxAssets caps assets per response, defending the fan-out; 0 uses the default.
	MaxAssets int

	// cache is an optional short-TTL response cache keyed by (limit, in).
	cache *cache.TTL[rwaOverviewDTO]
}

// NewRWAOverviewDeps wires the overview handler with an optional short-TTL
// response cache (cacheTTL <= 0 disables it), which amortises the per-asset
// fan-out across a polling dashboard's near-identical requests.
func NewRWAOverviewDeps(
	pairs RWAPairsLister,
	launches RWALaunchLister,
	change *apiprices.ChangeService,
	charts *apiprices.ChartService,
	converter apiprices.PriceConverter,
	source prices.Source,
	cacheTTL time.Duration,
) RWAOverviewDeps {
	return RWAOverviewDeps{
		Pairs:     pairs,
		Launches:  launches,
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
// Enabled RWA assets with dynamic data; an empty catalog returns
// `{ "assets": [] }`, not 404.
//
//   - limit (optional) — cap the number of assets; clamped to the server cap.
//   - in    (optional) — single FX target; converts the latest price and every
//     mini-series point. >1 currency → 400, mirroring the chart endpoints.
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

// RWALaunchLister supplies primary-issuance assets — launchpad tokens with no
// orderbook and therefore no rwa_pairs row. A nil lister omits them.
type RWALaunchLister interface {
	EnabledLaunches(ctx context.Context, source prices.Source) ([]prices.RWALaunch, error)
}

// assemble unions the secondary-market pairs with the primary-market launches,
// orders and truncates that union, and only then does the per-asset work —
// truncating pairs first would make `limit` unfair to primary-market assets.
// Assets are keyed by `{base}-{quote}`, NOT by token address: one token can be
// paired against several quotes, which an address key would collapse into one.
func (d RWAOverviewDeps) assemble(ctx context.Context, limit int, in []prices.Currency) (rwaOverviewDTO, error) {
	pairs, err := d.Pairs.EnabledRWAPairs(ctx)
	if err != nil {
		return rwaOverviewDTO{}, err
	}

	type entry struct {
		symbol string
		pair   *prices.RWAPair
		launch *prices.RWALaunch
	}
	index := make(map[string]*entry)
	upsert := func(symbol string) *entry {
		if e, ok := index[symbol]; ok {
			return e
		}
		e := &entry{symbol: symbol}
		index[symbol] = e
		return e
	}

	for i := range pairs {
		if pairs[i].Source != d.Source {
			continue
		}
		upsert(overviewSymbol(pairs[i])).pair = &pairs[i]
	}
	// Launches join the SAME entry when the symbol already trades, so a listed
	// asset still in issuance is returned once carrying both facets.
	for _, l := range d.enabledLaunches(ctx) {
		upsert(l.Symbol()).launch = &l //nolint:exportloopref // Go 1.22 per-iteration variable
	}

	entries := make([]*entry, 0, len(index))
	for _, e := range index {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].symbol < entries[j].symbol })
	if len(entries) > limit {
		entries = entries[:limit]
	}

	now := time.Now()
	assets := make([]rwaOverviewAsset, len(entries))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(overviewConcurrency)
	for i := range entries {
		i, e := i, entries[i]
		g.Go(func() error {
			// Launch-only asset: no orderbook, so no change/series query at all.
			if e.pair == nil {
				assets[i] = d.launchToAsset(gctx, *e.launch, in)
				return nil
			}
			asset, err := d.buildAsset(gctx, *e.pair, now, in)
			if err != nil {
				return err
			}
			// Both facets: the live quote stays the top-level price and the
			// issuance block carries its own sale price, so neither is lost.
			if e.launch != nil {
				asset.Issuance = issuanceBlock(*e.launch)
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

// enabledLaunches reads the primary-market catalog, degrading to an empty slice:
// a launch-catalog failure must not fail the whole response.
func (d RWAOverviewDeps) enabledLaunches(ctx context.Context) []prices.RWALaunch {
	if d.Launches == nil {
		return nil
	}
	launches, err := d.Launches.EnabledLaunches(ctx, d.Source)
	if err != nil {
		return nil
	}
	return launches
}

// launchToAsset renders one primary-issuance launch in the same asset shape as
// an orderbook pair, so a client iterates one list; series/change stay empty
// because no trades exist yet. `?in=` applies here as it does to a quote.
func (d RWAOverviewDeps) launchToAsset(ctx context.Context, l prices.RWALaunch, in []prices.Currency) rwaOverviewAsset {
	price := newNum6(l.Price)
	priceAsOf := nullableRFC3339(l.LastSyncedAt)
	asset := rwaOverviewAsset{
		Symbol:       l.Symbol(),
		Base:         strings.ToLower(l.BaseSymbol),
		Quote:        strings.ToLower(l.QuoteSymbol),
		NativeQuote:  strings.ToLower(l.QuoteSymbol),
		TokenAddress: nilIfEmpty(l.TokenAddr),
		Market:       marketPrimary,
		Price:        &price,
		PriceAsOf:    priceAsOf,
		Series:       seriesMiniDTO{Interval: string(overviewSeriesInterval), Points: []SeriesPointDTO{}},
		Issuance:     issuanceBlock(l),
	}
	if len(in) > 0 && d.Converter != nil {
		if quoteToken, resolved := promoteQuoteToken(l.QuoteSymbol); resolved {
			asset.Converted, asset.FX = convertNowFlat(ctx, d.Converter, quoteToken, in, l.Price, launchFXTime(l))
		}
	}
	return asset
}

// launchFXTime is when a launch's price converts: the launchpad read time, also
// the reported `price_as_of`. The zero value falls back to now — an at-or-before
// lookup against a zero timestamp would drop every target.
func launchFXTime(l prices.RWALaunch) time.Time {
	if l.LastSyncedAt.IsZero() {
		return time.Now()
	}
	return l.LastSyncedAt
}

// nullableRFC3339Ptr formats an optional timestamp for the wire.
func nullableRFC3339Ptr(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}

// overviewCacheKey partitions cached responses by asset cap and FX target.
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

// buildAsset assembles one asset's dynamic block; missing data renders as nulls
// or an empty series and the asset still appears in the list.
func (d RWAOverviewDeps) buildAsset(ctx context.Context, pair prices.RWAPair, now time.Time, in []prices.Currency) (rwaOverviewAsset, error) {
	nativeQuote := strings.ToLower(pair.QuoteSymbol)
	asset := rwaOverviewAsset{
		Symbol:       overviewSymbol(pair),
		Base:         strings.ToLower(pair.BaseSymbol),
		Quote:        nativeQuote,
		NativeQuote:  nativeQuote,
		TokenAddress: nilIfEmpty(pair.TokenAddr),
		Market:       marketSecondary,
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
					asset.Converted, asset.FX = convertNowFlat(ctx, d.Converter, quoteToken, in, cur.Now, cur.NowTS)
				}
			}
		}
		if cfp, ok := cur.ByPeriod[prices.Period24h]; ok && cfp.AnchorFound && cfp.ChangePctValid {
			da := newNum6(cfp.DeltaAbs)
			cp := newNum6(cfp.ChangePct)
			asset.Change24h = change24hDTO{DeltaAbs: &da, ChangePct: &cp}
		}
	}

	// Mini series (24h/15m). An unregistered native quote drops `?in=` here —
	// no FX source — matching the chart endpoint.
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

// nilIfEmpty renders an empty string as JSON `null` rather than "".
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

// rwaOverviewAsset is one row of the overview. `?in=` conversions inline as flat
// top-level numeric keys, matching /v1/rwa/:symbol/latest.
type rwaOverviewAsset struct {
	Symbol       string
	Base         string
	Quote        string
	NativeQuote  string
	TokenAddress *string // on-chain RWA token contract; null when not synced yet
	Market       string  // secondary | primary
	Issuance     *primaryIssuanceDTO
	Price        *num6 // null until a `last` tick exists
	PriceAsOf    *string
	Change24h    change24hDTO
	Series       seriesMiniDTO
	Converted    map[string]num6 // ?in= per-target latest price; nil when absent
	// FX carries stale-rate flags per converted currency; omitted when fresh.
	FX map[string]fxMetaDTO
}

func (a rwaOverviewAsset) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 11+len(a.Converted))
	out["symbol"] = a.Symbol
	out["market"] = a.Market
	out["base"] = a.Base
	out["quote"] = a.Quote
	out["native_quote"] = a.NativeQuote
	out["token_address"] = a.TokenAddress
	out["price"] = a.Price
	out["price_as_of"] = a.PriceAsOf
	out["change_24h"] = a.Change24h
	out["series_1d"] = a.Series
	if a.Issuance != nil {
		out["primary_issuance"] = a.Issuance
	}
	for cur, v := range a.Converted {
		out[cur] = v
	}
	if len(a.FX) > 0 {
		out["fx"] = a.FX
	}
	return json.Marshal(out)
}

// change24hDTO is the 24h change block: delta_abs in the native quote,
// change_pct unitless. Both null when the anchor is missing (Decision #10).
type change24hDTO struct {
	DeltaAbs  *num6 `json:"delta_abs"`
	ChangePct *num6 `json:"change_pct"`
}

// primaryIssuanceDTO is the launchpad block. total_bought / max_amount_cap stay
// raw on-chain nat strings because supply-scale values overflow float64;
// progress_percent is computed server-side and stays float so 2.667e-7 survives.
type primaryIssuanceDTO struct {
	LaunchID int    `json:"launch_id"`
	Name     string `json:"name"`
	Status   string `json:"status"` // active | inactive | paused | closed
	Active   bool   `json:"active"` // purchasable right now
	// Price is the base-tier sale price. It lives INSIDE this block because a
	// listed asset shows the live quote at the top level.
	Price           num6    `json:"price"`
	PriceAsOf       *string `json:"price_as_of"`
	TotalBought     string  `json:"total_bought"`
	MaxAmountCap    string  `json:"max_amount_cap"`
	ProgressPercent float64 `json:"progress_percent"`
	SaleStart       *string `json:"sale_start"`
	SaleEnd         *string `json:"sale_end"`
}

// issuanceBlock renders the launch facet, shared by the list and per-symbol endpoints.
func issuanceBlock(l prices.RWALaunch) *primaryIssuanceDTO {
	return &primaryIssuanceDTO{
		LaunchID:        l.LaunchID,
		Name:            l.Name,
		Status:          l.Status,
		Active:          l.Active,
		Price:           newNum6(l.Price),
		PriceAsOf:       nullableRFC3339(l.LastSyncedAt),
		TotalBought:     l.TotalBought.String(),
		MaxAmountCap:    l.MaxAmountCap.String(),
		ProgressPercent: l.ProgressPercent,
		SaleStart:       nullableRFC3339Ptr(l.SaleStart),
		SaleEnd:         nullableRFC3339Ptr(l.SaleEnd),
	}
}

// seriesMiniDTO is the short 1-day chart; it reuses SeriesPointDTO so clients
// parse the same point shape as /v1/rwa/:symbol/series.
type seriesMiniDTO struct {
	Interval string           `json:"interval"`
	Points   []SeriesPointDTO `json:"points"`
}
