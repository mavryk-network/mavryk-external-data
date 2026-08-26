package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/metrics"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// ChangeDeps wires both FT and RWA change handlers. The same ChangeService
// type is reused for both classes (per Decision #5: opaque ChangeRepository
// interface, one service serves both); only the Repo and Kind label differ
// at construction time.
//
// FT — `FTService` runs over a TokenChangeRepository.
// RWA — `RWAService` runs over an RWAChangeRepository. `Converter` (when
// set) enables `?in=usd,eur,...` on `/v1/rwa/:symbol/change` (Decision #19).
type ChangeDeps struct {
	FTService     *apiprices.ChangeService
	RWAService    *apiprices.ChangeService
	Lookup        PairLookup
	Converter     apiprices.PriceConverter // optional; when nil, ?in= returns 400
	DefaultSource prices.Source            // typically prices.SourceCoinGecko for FT
	RWASource     prices.Source            // typically prices.SourceEquiteez for RWA
	// MaxPeriods caps the number of periods accepted in ?periods=.
	// 0 means use len(prices.AllPeriods) — the full whitelist.
	MaxPeriods int
	// MaxInCurrencies caps the number of comma-separated currencies in
	// ?in= for the RWA endpoint. 0 falls back to 10.
	MaxInCurrencies int
}

// ChangeFT — GET /v1/prices/:token/change
//
// Query params:
//   - currency (optional, csv)  — defaults to all 10 supported currencies
//   - periods  (optional, csv)  — defaults to 24h,7d,30d
func (d ChangeDeps) ChangeFT() gin.HandlerFunc {
	type request struct {
		TokenSymbol string
		Currencies  []string
		Periods     []prices.Period
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
		periods, err := parsePeriodsParam(c, d.maxPeriods())
		if err != nil {
			return request{}, err
		}
		curStrings, err := parseFTCurrenciesParam(c)
		if err != nil {
			return request{}, err
		}
		return request{TokenSymbol: string(t), Currencies: curStrings, Periods: periods}, nil
	}
	action := func(ctx context.Context, req request) (ftChangeDTO, error) {
		res, err := d.FTService.GetChange(ctx, apiprices.ChangeQuery{
			Source:     d.DefaultSource,
			EntityKey:  req.TokenSymbol,
			Currencies: req.Currencies,
			Periods:    req.Periods,
			Now:        time.Now(),
		})
		if err != nil {
			return ftChangeDTO{}, err
		}
		return renderFTChange(req.TokenSymbol, req.Periods, req.Currencies, res), nil
	}
	return common.Wrap(bind, action)
}

// ChangeRWA — GET /v1/rwa/:symbol/change
//
// Query params:
//   - periods (optional, csv) — defaults to 24h,7d,30d
//   - in (optional, csv)      — read-side FX conversion of `now`. Each
//     target gets the at-or-before rate matching the `now` timestamp
//     (Decision #19). Per-target failures drop the key silently;
//     the request stays 200 as long as the native price was found.
func (d ChangeDeps) ChangeRWA() gin.HandlerFunc {
	type request struct {
		Pair      prices.RWAPair
		Periods   []prices.Period
		InTargets []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		raw := c.Param("symbol")
		base, quote, ok := parseRWASymbol(raw)
		if !ok {
			return request{}, coreerrors.InvalidArgument("symbol must be {base}-{quote}, e.g. mars1-usdt")
		}
		pair, err := d.Lookup.LookupRWAPairBySymbol(c.Request.Context(), base, quote)
		if err != nil {
			return request{}, err
		}
		periods, err := parsePeriodsParam(c, d.maxPeriods())
		if err != nil {
			return request{}, err
		}
		inTargets, err := d.parseRWAInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{Pair: pair, Periods: periods, InTargets: inTargets}, nil
	}
	action := func(ctx context.Context, req request) (rwaChangeDTO, error) {
		nativeQuote := strings.ToLower(req.Pair.QuoteSymbol)
		res, err := d.RWAService.GetChange(ctx, apiprices.ChangeQuery{
			Source:     d.RWASource,
			EntityKey:  req.Pair.EntityKey(),
			AuxKey:     lastSide,
			Currencies: []string{nativeQuote},
			Periods:    req.Periods,
			Now:        time.Now(),
		})
		if err != nil {
			return rwaChangeDTO{}, err
		}
		symbol := strings.ToLower(req.Pair.BaseSymbol) + "-" + nativeQuote

		// Optional ?in= conversion of `now`. Uses converter's at-or-before
		// semantics (Decision #19). Failed conversions drop silently.
		var converted map[string]num6
		var fxMeta map[string]fxMetaDTO
		if len(req.InTargets) > 0 && d.Converter != nil {
			cur, hasCur := res.Currencies[nativeQuote]
			if hasCur && cur.NowFound {
				quoteToken, quoteResolved := promoteQuoteToken(req.Pair.QuoteSymbol)
				if quoteResolved {
					converted, fxMeta = convertNowFlat(ctx, d.Converter, quoteToken, req.InTargets, cur.Now, cur.NowTS)
				}
			}
		}
		return renderRWAChange(symbol, nativeQuote, req.Periods, converted, fxMeta, res), nil
	}
	return common.Wrap(bind, action)
}

// parseRWAInQuery validates the `?in=` parameter against the supported
// currency registry and the configured cap (MaxInCurrencies). Mirrors the
// existing RWAPriceDeps.parseInQuery convention.
func (d ChangeDeps) parseRWAInQuery(c *gin.Context) ([]prices.Currency, error) {
	raw := strings.TrimSpace(c.Query("in"))
	if raw == "" {
		return nil, nil
	}
	if d.Converter == nil {
		return nil, coreerrors.InvalidArgument("'?in=' is not enabled on this server")
	}
	parts := strings.Split(raw, ",")
	maxIn := d.MaxInCurrencies
	if maxIn <= 0 {
		maxIn = 10
	}
	if len(parts) > maxIn {
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

// convertNowFlat returns a per-target-currency map for the `now` price plus
// stale-rate flags for the `fx` block. Failed conversions (no FX rate,
// unsupported target) drop silently, just like the existing
// /v1/rwa/:symbol/latest?in= flow. Returns nil when no target succeeded so
// the response stays clean.
//
// Conversions go through timedConvert so the fx_* metric families (outcome,
// duration, stale ratio) cover these edges too — this helper serves GET /v1/rwa
// (every asset in the list) and /change, which a polling dashboard hits far
// more often than the per-symbol endpoints convertFlat meters.
func convertNowFlat(
	ctx context.Context,
	conv apiprices.PriceConverter,
	quoteToken prices.Token,
	targets []prices.Currency,
	nativePrice decimal.Decimal,
	nativeTS time.Time,
) (map[string]num6, map[string]fxMetaDTO) {
	out := make(map[string]num6, len(targets))
	var fx map[string]fxMetaDTO
	for _, target := range targets {
		res, err := timedConvert(ctx, conv, quoteToken, target, nativePrice, nativeTS)
		if err != nil {
			continue
		}
		out[string(target)] = newNum6(res.Amount)
		if res.Stale {
			metrics.FXStaleResponsesTotal.WithLabelValues(string(target)).Inc()
			if fx == nil {
				fx = make(map[string]fxMetaDTO, 1)
			}
			fx[string(target)] = fxMetaDTO{RateTS: res.RateTS.UTC().Format(time.RFC3339), Stale: true}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, fx
}

func (d ChangeDeps) maxPeriods() int {
	if d.MaxPeriods > 0 {
		return d.MaxPeriods
	}
	return len(prices.AllPeriods)
}

// --- query parsing ---

// parsePeriodsParam reads ?periods=<csv>, defaults to DefaultChangePeriods,
// and validates each entry against the whitelist.
func parsePeriodsParam(c *gin.Context, maxCount int) ([]prices.Period, error) {
	raw := strings.TrimSpace(c.Query("periods"))
	if raw == "" {
		// Default contract per design doc: 24h,7d,30d.
		out := make([]prices.Period, len(prices.DefaultChangePeriods))
		copy(out, prices.DefaultChangePeriods)
		return out, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxCount {
		return nil, coreerrors.InvalidArgument("Too many periods; cap is configured server-side")
	}
	seen := make(map[prices.Period]struct{}, len(parts))
	out := make([]prices.Period, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		period, ok := prices.ParsePeriod(p)
		if !ok {
			return nil, coreerrors.InvalidArgument("Invalid 'periods' value: " + p)
		}
		if _, dup := seen[period]; dup {
			continue
		}
		seen[period] = struct{}{}
		out = append(out, period)
	}
	if len(out) == 0 {
		return nil, coreerrors.InvalidArgument("'periods' must contain at least one valid value")
	}
	return out, nil
}

// parseFTCurrenciesParam reads ?currency=<csv> and validates each value
// against the closed Currency enum. Empty / missing returns the canonical
// 10-currency list, matching /latest's "all currencies" semantics.
func parseFTCurrenciesParam(c *gin.Context) ([]string, error) {
	raw := strings.TrimSpace(c.Query("currency"))
	if raw == "" {
		all := prices.AllSupportedCurrencies()
		out := make([]string, len(all))
		for i, cur := range all {
			out[i] = string(cur)
		}
		return out, nil
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p == "" {
			continue
		}
		cur, err := prices.NewCurrency(p)
		if err != nil {
			return nil, coreerrors.InvalidArgument("Invalid 'currency' value: " + p)
		}
		s := string(cur)
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, coreerrors.InvalidArgument("'currency' must contain at least one valid value")
	}
	return out, nil
}

// --- DTOs ---

// ftChangeDTO is the wire shape of GET /v1/prices/:token/change.
//
//	{
//	  "token": "mvrk",
//	  "as_of": "2026-05-08T12:00:00Z",
//	  "currencies": {
//	    "usd": {
//	      "now": 0.071541,
//	      "periods": {
//	        "24h": {"from_ts": "...", "from": ..., "delta_abs": ..., "change_pct": ...},
//	        ...
//	      }
//	    }
//	  }
//	}
type ftChangeDTO struct {
	Token      string                            `json:"token"`
	AsOf       *string                           `json:"as_of"`
	Currencies map[string]ftChangeForCurrencyDTO `json:"currencies"`
}

type ftChangeForCurrencyDTO struct {
	Now     *num6             `json:"now"`
	Periods orderedPeriodsDTO `json:"periods"`
}

// orderedPeriodsDTO renders the per-period block as a JSON object whose
// keys appear in the client-requested order (or DefaultChangePeriods when
// omitted). Necessary because Go map iteration is randomised — without
// this wrapper, snapshot tests and human readers see periods in arbitrary
// order. JSON spec doesn't require key order, so this is a UX improvement,
// not a contract change.
type orderedPeriodsDTO struct {
	order []prices.Period
	data  map[prices.Period]ftChangePeriodDTO
}

// MarshalJSON emits keys in `order`. Missing keys (a Period in `order`
// that isn't in `data`) emit a default-value (`null`-everything) period
// block — defensive, since the renderer always populates every requested
// period.
func (o orderedPeriodsDTO) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range o.order {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(string(p))
		if err != nil {
			return nil, err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		valJSON, err := json.Marshal(o.data[p])
		if err != nil {
			return nil, err
		}
		buf.Write(valJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// ftChangePeriodDTO uses pointer-num6 for the nullable fields so the
// JSON renders `null` instead of `0` when the anchor is missing or
// p_then is zero.
type ftChangePeriodDTO struct {
	FromTS    *string `json:"from_ts"`
	From      *num6   `json:"from"`
	DeltaAbs  *num6   `json:"delta_abs"`
	ChangePct *num6   `json:"change_pct"`
}

// rwaChangeDTO is the wire shape of GET /v1/rwa/:symbol/change.
//
//	{
//	  "symbol": "mars1-usdt",
//	  "native_quote": "usdt",
//	  "as_of": "2026-05-08T12:00:00Z",
//	  "now": 56.25,
//	  "periods": { "24h": {...} }
//	}
type rwaChangeDTO struct {
	Symbol      string            `json:"symbol"`
	NativeQuote string            `json:"native_quote"`
	AsOf        *string           `json:"as_of"`
	Now         *num6             `json:"now"`
	Periods     orderedPeriodsDTO `json:"periods"`
	// Converted is the per-currency map populated when ?in= succeeds.
	// Renders as flat top-level numeric keys (one per requested target)
	// — same convention as /v1/rwa/:symbol/latest. Empty map omits the
	// keys entirely.
	Converted map[string]num6 `json:"-"`
	// FX carries stale-rate flags per converted currency; omitted when fresh.
	FX map[string]fxMetaDTO `json:"-"`
}

// MarshalJSON for rwaChangeDTO flattens Converted onto the top-level
// object so a client reads `response.usd` (number) instead of
// `response.converted.usd`. Currency codes are 3 letters and never
// collide with reserved keys (`symbol`, `native_quote`, `as_of`,
// `now`, `periods`).
func (d rwaChangeDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 5+len(d.Converted))
	out["symbol"] = d.Symbol
	out["native_quote"] = d.NativeQuote
	out["as_of"] = d.AsOf
	out["now"] = d.Now
	out["periods"] = d.Periods
	for cur, v := range d.Converted {
		out[cur] = v
	}
	if len(d.FX) > 0 {
		out["fx"] = d.FX
	}
	return json.Marshal(out)
}

// --- rendering ---

func renderFTChange(token string, periods []prices.Period, currencies []string, res apiprices.ChangeResult) ftChangeDTO {
	out := ftChangeDTO{
		Token:      token,
		AsOf:       nullableRFC3339(res.AsOf),
		Currencies: make(map[string]ftChangeForCurrencyDTO, len(currencies)),
	}
	for _, cur := range currencies {
		c, ok := res.Currencies[cur]
		if !ok {
			// The service composes an entry for every requested currency,
			// so this branch is defensive — keep the key with all-nulls.
			out.Currencies[cur] = ftChangeForCurrencyDTO{
				Periods: orderedPeriodsDTO{order: periods, data: emptyPeriodsData(periods)},
			}
			continue
		}
		var nowVal *num6
		if c.NowFound {
			n := newNum6(c.Now)
			nowVal = &n
		}
		out.Currencies[cur] = ftChangeForCurrencyDTO{
			Now:     nowVal,
			Periods: orderedPeriodsDTO{order: periods, data: renderPeriodsData(periods, c.ByPeriod)},
		}
	}
	return out
}

func renderRWAChange(symbol, nativeQuote string, periods []prices.Period, converted map[string]num6, fx map[string]fxMetaDTO, res apiprices.ChangeResult) rwaChangeDTO {
	cur, ok := res.Currencies[nativeQuote]
	if !ok {
		// Defensive — service always emits the requested currency entry.
		return rwaChangeDTO{
			Symbol:      symbol,
			NativeQuote: nativeQuote,
			AsOf:        nullableRFC3339(res.AsOf),
			Periods:     orderedPeriodsDTO{order: periods, data: emptyPeriodsData(periods)},
			Converted:   converted,
			FX:          fx,
		}
	}
	var nowVal *num6
	if cur.NowFound {
		n := newNum6(cur.Now)
		nowVal = &n
	}
	return rwaChangeDTO{
		Symbol:      symbol,
		NativeQuote: nativeQuote,
		AsOf:        nullableRFC3339(res.AsOf),
		Now:         nowVal,
		Periods:     orderedPeriodsDTO{order: periods, data: renderPeriodsData(periods, cur.ByPeriod)},
		Converted:   converted,
		FX:          fx,
	}
}

func renderPeriodsData(periods []prices.Period, by map[prices.Period]apiprices.ChangeForPeriod) map[prices.Period]ftChangePeriodDTO {
	out := make(map[prices.Period]ftChangePeriodDTO, len(periods))
	for _, p := range periods {
		out[p] = renderPeriodDTO(by[p])
	}
	return out
}

func renderPeriodDTO(cfp apiprices.ChangeForPeriod) ftChangePeriodDTO {
	if !cfp.AnchorFound {
		// Anchor missing → all four fields null per Decision #3.
		return ftChangePeriodDTO{}
	}
	ts := formatRFC3339(cfp.FromTS)
	from := newNum6(cfp.FromPrice)
	dto := ftChangePeriodDTO{
		FromTS: &ts,
		From:   &from,
	}
	if cfp.ChangePctValid {
		da := newNum6(cfp.DeltaAbs)
		cp := newNum6(cfp.ChangePct)
		dto.DeltaAbs = &da
		dto.ChangePct = &cp
	}
	// If !ChangePctValid (p_then == 0): from/from_ts populated, delta_abs +
	// change_pct stay nil → render as null. Decision #10.
	return dto
}

func emptyPeriodsData(periods []prices.Period) map[prices.Period]ftChangePeriodDTO {
	out := make(map[prices.Period]ftChangePeriodDTO, len(periods))
	for _, p := range periods {
		out[p] = ftChangePeriodDTO{}
	}
	return out
}

// nullableRFC3339 returns nil for the zero time (so JSON renders `null`)
// and a pointer to the formatted UTC string otherwise. Used for `as_of`
// when no currency has data yet — Decision #20.
func nullableRFC3339(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}

func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
