package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/metrics"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// maxSymbolLen caps `{base}-{quote}` URL length defensively. Sync produces
// short tickers; anything longer is gateway-grade input noise.
const maxSymbolLen = 64

// lastSide is the only orderbook side surfaced on the read-side. bid/ask/mid
// stay in storage for future endpoints (e.g. /orderbook), but are not part
// of this contract — which keeps the response flat and the SQL narrow.
const lastSide = "last"

// PairLookup is the read-only contract handlers need to resolve a pair
// `{base}-{quote}` symbol into the canonical RWAPair (used for both the
// price query EntityKey and the `?in=` quote-token resolution).
type PairLookup interface {
	LookupRWAPairBySymbol(ctx context.Context, base, quote string) (prices.RWAPair, error)
}

// RWAStatsReader supplies the derived per-pair stats that enrich the /latest
// snapshot: the all-time-high (price + date) and the price one year ago. Both
// are read on the `last` side. Optional on RWAPriceDeps — when the reader is
// nil, /latest simply omits the `ath` / `price_one_year_ago` blocks.
type RWAStatsReader interface {
	AllTimeHighLast(ctx context.Context, pairID int64, side string) (decimal.Decimal, time.Time, bool, error)
	PriceAtOrBefore(ctx context.Context, pairID int64, side string, ts time.Time) (decimal.Decimal, time.Time, bool, error)
}

// RWAPriceDeps wires the RWA-side dependencies.
type RWAPriceDeps struct {
	Service       apiprices.QueryService
	Lookup        PairLookup               // required: resolves `{base}-{quote}` -> RWAPair
	Converter     apiprices.PriceConverter // optional; when nil, `?in=` returns 400
	DefaultSource prices.Source            // typically prices.SourceEquiteez
	MaxLimit      int
	DefaultLimit  int
	// MaxInCurrencies caps the number of comma-separated currencies in `?in=`.
	// 0 means unlimited (not recommended); production should pin to e.g. 10.
	MaxInCurrencies int
	// Stats is optional; supplies ath + price-one-year-ago for /latest. When
	// nil those blocks are omitted (keeps the endpoint back-compatible and lets
	// unit tests that don't exercise them stay minimal).
	Stats RWAStatsReader
}

// ListBySymbol — GET /v1/rwa/:symbol
//
// Returns an array of RWAPrice objects (one per timestamp, only `last`
// side). Optional `?in=usd,eur,...` adds converted prices as flat
// top-level keys per row.
func (d RWAPriceDeps) ListBySymbol() gin.HandlerFunc {
	type request struct {
		Pair      prices.RWAPair
		Query     prices.Query
		InTargets []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		pair, err := d.resolvePairFromPath(c)
		if err != nil {
			return request{}, err
		}
		// MetricParam: "" disables `?side=` parsing — only `last` is surfaced.
		opts := common.QueryOptions{
			MaxLimit:           d.MaxLimit,
			DefaultLatestLimit: d.DefaultLimit,
		}
		pq, err := common.BindPriceQuery(c, opts)
		if err != nil {
			return request{}, err
		}
		inTargets, err := d.parseInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{
			Pair: pair,
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: pair.EntityKey(),
				Metrics:   []string{lastSide},
				From:      pq.From,
				To:        pq.To,
				Limit:     pq.Limit,
			},
			InTargets: inTargets,
		}, nil
	}
	action := func(ctx context.Context, req request) ([]rwaPriceDTO, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return nil, err
		}
		quoteToken, quoteResolved := promoteQuoteToken(req.Pair.QuoteSymbol)
		nativeQuote := strings.ToLower(req.Pair.QuoteSymbol)
		out := make([]rwaPriceDTO, 0, len(points))
		for _, p := range points {
			row := rwaPriceDTO{
				Timestamp:   p.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				NativeQuote: nativeQuote,
				Price:       newNum6(p.Price),
			}
			if len(req.InTargets) > 0 {
				row.Converted = d.convertFlat(ctx, quoteToken, quoteResolved, req.InTargets, p.Price, p.Timestamp)
			}
			out = append(out, row)
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// LatestBySymbol — GET /v1/rwa/:symbol/latest
//
// Returns one RWAPrice object — same shape as a single element of
// ListBySymbol's array. 404 when the pair has no `last` row yet.
func (d RWAPriceDeps) LatestBySymbol() gin.HandlerFunc {
	type request struct {
		Pair      prices.RWAPair
		Query     prices.Query
		InTargets []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		pair, err := d.resolvePairFromPath(c)
		if err != nil {
			return request{}, err
		}
		inTargets, err := d.parseInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{
			Pair: pair,
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: pair.EntityKey(),
				Metrics:   []string{lastSide},
				// Empty From/To ⇒ IsLatest ⇒ DISTINCT ON (side) DESC; with
				// Metrics narrowed to [last] only one row comes back.
			},
			InTargets: inTargets,
		}, nil
	}
	action := func(ctx context.Context, req request) (rwaPriceDTO, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return rwaPriceDTO{}, err
		}
		picked, ok := pickLatestLast(points)
		if !ok {
			return rwaPriceDTO{}, coreerrors.NotFound("No prices for pair")
		}
		quoteToken, quoteResolved := promoteQuoteToken(req.Pair.QuoteSymbol)
		dto := rwaPriceDTO{
			Timestamp:   picked.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			NativeQuote: strings.ToLower(req.Pair.QuoteSymbol),
			Price:       newNum6(picked.Price),
		}
		if len(req.InTargets) > 0 {
			dto.Converted = d.convertFlat(ctx, quoteToken, quoteResolved, req.InTargets, picked.Price, picked.Timestamp)
		}
		d.enrichLatest(ctx, &dto, req.Pair, quoteToken, quoteResolved, req.InTargets, picked.Timestamp)
		return dto, nil
	}
	return common.Wrap(bind, action)
}

// enrichLatest adds the `ath` and `price_one_year_ago` blocks to a /latest DTO
// when a Stats reader is configured. Each block carries the native-quote value
// plus — when `?in=` targets are present — the same flat per-currency
// conversions as the top-level price. Conversions use the snapshot's own FX
// rate (quoteTS, the latest `last` tick time), matching the top-level price's
// rate, so the converted ATH / year-ago values are always present whenever the
// top-level conversions are (rather than being dropped for lack of FX history
// at the historical block date). The `ath.date` field still reports when the
// ATH actually occurred. A nil reader, missing data, or a repo error simply
// omits the block: the core price snapshot still returns 200.
func (d RWAPriceDeps) enrichLatest(
	ctx context.Context,
	dto *rwaPriceDTO,
	pair prices.RWAPair,
	quoteToken prices.Token,
	quoteResolved bool,
	targets []prices.Currency,
	quoteTS time.Time,
) {
	if d.Stats == nil {
		return
	}
	if price, ts, found, err := d.Stats.AllTimeHighLast(ctx, pair.ID, lastSide); err == nil && found {
		ath := &athDTO{Price: newNum6(price), Date: ts.Format("2006-01-02")}
		if len(targets) > 0 {
			ath.Converted = d.convertFlat(ctx, quoteToken, quoteResolved, targets, price, quoteTS)
		}
		dto.ATH = ath
	}
	yearAgo := time.Now().UTC().AddDate(-1, 0, 0)
	if price, _, found, err := d.Stats.PriceAtOrBefore(ctx, pair.ID, lastSide, yearAgo); err == nil && found {
		p1y := &priceAtDTO{Price: newNum6(price)}
		if len(targets) > 0 {
			p1y.Converted = d.convertFlat(ctx, quoteToken, quoteResolved, targets, price, quoteTS)
		}
		dto.PriceOneYearAgo = p1y
	}
}

// --- DTO ---

// rwaPriceDTO is the on-wire shape of a single RWA price observation.
// Used identically by the list and latest endpoints — only the cardinality
// differs (array vs single object).
//
// Top-level keys are `timestamp`, `native_quote`, `price`, plus one
// numeric key per converted currency. The converted currencies are
// inlined via custom MarshalJSON so the response stays flat. ISO-4217
// codes are 3 letters and never collide with the reserved keys.
type rwaPriceDTO struct {
	Timestamp   string
	NativeQuote string
	Price       num6
	Converted   map[string]num6 // optional; one key per `?in=` target that succeeded
	// Optional enrichment (only on /latest, only when a Stats reader is wired).
	// `ath` / `price_one_year_ago` are nested objects (not flat currency keys)
	// so they don't collide with the top-level converted-price currency keys.
	ATH             *athDTO
	PriceOneYearAgo *priceAtDTO
}

func (d rwaPriceDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 3+len(d.Converted))
	out["timestamp"] = d.Timestamp
	out["native_quote"] = d.NativeQuote
	out["price"] = d.Price
	for cur, v := range d.Converted {
		out[cur] = v
	}
	if d.ATH != nil {
		out["ath"] = d.ATH
	}
	if d.PriceOneYearAgo != nil {
		out["price_one_year_ago"] = d.PriceOneYearAgo
	}
	return json.Marshal(out)
}

// athDTO is the all-time-high block on /latest: native-quote `price`, the
// `date` (YYYY-MM-DD) it occurred, plus one numeric key per `?in=` currency
// (converted at the snapshot's current FX rate, same as the top-level price).
// Same flat-currency-inline convention as rwaPriceDTO, scoped inside the `ath`
// object.
type athDTO struct {
	Price     num6
	Date      string
	Converted map[string]num6
}

func (a athDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 2+len(a.Converted))
	out["price"] = a.Price
	out["date"] = a.Date
	for cur, v := range a.Converted {
		out[cur] = v
	}
	return json.Marshal(out)
}

// priceAtDTO is a bare price block (price-one-year-ago): native-quote `price`
// plus one numeric key per `?in=` currency, converted at the snapshot's
// current FX rate (same as the top-level price).
type priceAtDTO struct {
	Price     num6
	Converted map[string]num6
}

func (p priceAtDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 1+len(p.Converted))
	out["price"] = p.Price
	for cur, v := range p.Converted {
		out[cur] = v
	}
	return json.Marshal(out)
}

// num6 carries a price rounded to 6 decimal places and serialises as a
// JSON number (no quotes). Stays in decimal precision — no float64
// round-trip — so values like 156.25 don't drift to 156.249999...
type num6 struct{ d decimal.Decimal }

func newNum6(d decimal.Decimal) num6 { return num6{d: d.Round(6)} }

func (n num6) MarshalJSON() ([]byte, error) {
	// decimal.String() is the canonical form: "34", "0.5", "124.857494".
	// No trailing zeros, no quotes — already a valid JSON number.
	return []byte(n.d.String()), nil
}

// --- internal helpers ---

// resolvePairFromPath parses `:symbol` and resolves it to a single enabled
// RWAPair. Returns:
//   - 400 INVALID_ARGUMENT — symbol does not parse as `{base}-{quote}`.
//   - 404 NOT_FOUND        — no enabled pair matches (via ErrPairNotFound).
//   - 409 CONFLICT         — 2+ enabled pairs match (via PairAmbiguousError).
func (d RWAPriceDeps) resolvePairFromPath(c *gin.Context) (prices.RWAPair, error) {
	raw := c.Param("symbol")
	base, quote, ok := parseRWASymbol(raw)
	if !ok {
		return prices.RWAPair{}, coreerrors.InvalidArgument(
			"symbol must be {base}-{quote}, e.g. mars1-usdt")
	}
	return d.Lookup.LookupRWAPairBySymbol(c.Request.Context(), base, quote)
}

// parseRWASymbol splits `{base}-{quote}` on the LAST hyphen so that future
// dashes inside `base_symbol` (e.g. `X-AT-USDT`) keep parsing. Returns
// lowercased components for case-insensitive SQL comparison.
func parseRWASymbol(s string) (base, quote string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxSymbolLen {
		return "", "", false
	}
	i := strings.LastIndex(s, "-")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	base = strings.ToLower(strings.TrimSpace(s[:i]))
	quote = strings.ToLower(strings.TrimSpace(s[i+1:]))
	if base == "" || quote == "" {
		return "", "", false
	}
	return base, quote, true
}

// pickLatestLast returns the single freshest `last` PricePoint. Defensive
// — the repository's latestPerSide already returns at most one row when
// Metrics is pinned to [last], so this is a tiebreaker for the (rare)
// case where stub or test data carries multiple last-rows.
func pickLatestLast(points []prices.PricePoint) (prices.PricePoint, bool) {
	var picked prices.PricePoint
	found := false
	for _, p := range points {
		if !strings.EqualFold(p.Metric, lastSide) {
			continue
		}
		if !found || p.Timestamp.After(picked.Timestamp) {
			picked = p
			found = true
		}
	}
	return picked, found
}

// promoteQuoteToken tries to lift the pair's quote_symbol into a registered
// Token. Returns (zero, false) when the symbol is unregistered — the
// flat-response handler then simply omits all converted-currency keys
// (we'd never get a usable rate without a registered source token).
func promoteQuoteToken(quoteSymbol string) (prices.Token, bool) {
	t, err := prices.NewToken(quoteSymbol)
	if err != nil {
		return "", false
	}
	return t, true
}

// parseInQuery validates the comma-separated `?in=` parameter against
// `prices.NewCurrency`. Empty / missing returns nil. Unknown values OR
// requests larger than MaxInCurrencies fail with 400 INVALID_ARGUMENT.
func (d RWAPriceDeps) parseInQuery(c *gin.Context) ([]prices.Currency, error) {
	raw := strings.TrimSpace(c.Query("in"))
	if raw == "" {
		return nil, nil
	}
	if d.Converter == nil || d.Lookup == nil {
		return nil, coreerrors.InvalidArgument("'?in=' is not enabled on this server")
	}
	parts := strings.Split(raw, ",")
	if d.MaxInCurrencies > 0 && len(parts) > d.MaxInCurrencies {
		return nil, coreerrors.InvalidArgument("Too many currencies in 'in'; cap is configured server-side")
	}
	out := make([]prices.Currency, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		cur, err := prices.NewCurrency(p)
		if err != nil {
			return nil, coreerrors.InvalidArgument("Invalid 'in' currency: " + p)
		}
		out = append(out, cur)
	}
	return out, nil
}

// convertFlat builds the per-currency map for one row. Failed conversions
// (no FX rate, unsupported target, unregistered source) are silently
// dropped from the output map — observability lives in the
// `fx_conversions_total` counter, not on the wire.
func (d RWAPriceDeps) convertFlat(
	ctx context.Context,
	quoteToken prices.Token,
	quoteResolved bool,
	targets []prices.Currency,
	native decimal.Decimal,
	ts time.Time,
) map[string]num6 {
	if !quoteResolved {
		// Account for the registry-miss in metrics for every requested target;
		// the wire response just omits the currency keys.
		for _, t := range targets {
			metrics.FXConversionsTotal.WithLabelValues("unknown", string(t), "unregistered_source").Inc()
		}
		return nil
	}
	out := make(map[string]num6, len(targets))
	for _, target := range targets {
		res, err := timedConvert(ctx, d.Converter, quoteToken, target, native, ts)
		if err != nil {
			continue // drop on error; metrics already incremented inside timedConvert
		}
		out[string(target)] = newNum6(res.Amount)
		if res.Stale {
			metrics.FXStaleResponsesTotal.WithLabelValues(string(target)).Inc()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// timedConvert wraps PriceConverter.Convert with timing + outcome metrics.
func timedConvert(
	ctx context.Context,
	conv apiprices.PriceConverter,
	sourceToken prices.Token,
	target prices.Currency,
	amount decimal.Decimal,
	ts time.Time,
) (apiprices.ConversionResult, error) {
	start := time.Now()
	res, err := conv.Convert(ctx, sourceToken, target, amount, ts)
	metrics.FXConversionDurationSeconds.WithLabelValues(string(sourceToken), string(target)).
		Observe(time.Since(start).Seconds())
	switch {
	case err == nil && res.Identity:
		metrics.FXConversionsTotal.WithLabelValues(string(sourceToken), string(target), "identity").Inc()
	case err == nil:
		metrics.FXConversionsTotal.WithLabelValues(string(sourceToken), string(target), "success").Inc()
	default:
		metrics.FXConversionsTotal.WithLabelValues(string(sourceToken), string(target), errToMetricLabel(err)).Inc()
	}
	return res, err
}

func errToMetricLabel(err error) string {
	switch {
	case stderrors.Is(err, apiprices.ErrNoFXRate):
		return "no_rate"
	case stderrors.Is(err, apiprices.ErrUnsupportedTargetCurrency):
		return "unsupported_target"
	case stderrors.Is(err, apiprices.ErrSourceTokenNotRegistered):
		return "unregistered_source"
	default:
		return "query_error"
	}
}

// Compile-time sanity: real *repositories.LookupRepository satisfies PairLookup.
var _ PairLookup = (*repositories.LookupRepository)(nil)
