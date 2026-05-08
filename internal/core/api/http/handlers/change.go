package handlers

import (
	"context"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
)

// ChangeDeps wires both FT and RWA change handlers. The same ChangeService
// type is reused for both classes (per Decision #5: opaque ChangeRepository
// interface, one service serves both); only the Repo and Kind label differ
// at construction time.
//
// FT — `Service` runs over a TokenChangeRepository.
// RWA — `Service` runs over an RWAChangeRepository.
type ChangeDeps struct {
	FTService     *apiprices.ChangeService
	RWAService    *apiprices.ChangeService
	Lookup        PairLookup
	DefaultSource prices.Source // typically prices.SourceCoinGecko for FT, prices.SourceEquiteez for RWA
	RWASource     prices.Source
	// MaxPeriods caps the number of periods accepted in ?periods=.
	// 0 means use len(prices.AllPeriods) — the full whitelist.
	MaxPeriods int
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
//   - periods (optional, csv)  — defaults to 24h,7d,30d
//   - in (any value)            — explicit 400 NOT_IMPLEMENTED until the
//     historical FX-rate fix lands (Decision #9 / #17).
func (d ChangeDeps) ChangeRWA() gin.HandlerFunc {
	type request struct {
		Pair    prices.RWAPair
		Periods []prices.Period
	}
	bind := func(c *gin.Context) (request, error) {
		// Refuse `?in=` early with a precise code so frontend can branch
		// (the bug it depends on is tracked in fix_todo.md).
		if strings.TrimSpace(c.Query("in")) != "" {
			return request{}, coreerrors.Wrap(
				codeNotImplemented,
				"'in' parameter is not yet supported on /change; deferred until historical FX-rate fix lands (see fix_todo.md)",
				nil,
			)
		}
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
		return request{Pair: pair, Periods: periods}, nil
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
		return renderRWAChange(symbol, nativeQuote, req.Periods, res), nil
	}
	return common.Wrap(bind, action)
}

func (d ChangeDeps) maxPeriods() int {
	if d.MaxPeriods > 0 {
		return d.MaxPeriods
	}
	return len(prices.AllPeriods)
}

// codeNotImplemented is the wire-stable error code for the deferred
// RWA `?in=` flow. Frontend matches against this exact string.
const codeNotImplemented coreerrors.Code = "NOT_IMPLEMENTED"

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
	AsOf       string                            `json:"as_of"`
	Currencies map[string]ftChangeForCurrencyDTO `json:"currencies"`
}

type ftChangeForCurrencyDTO struct {
	Now     *num6                        `json:"now"`
	Periods map[string]ftChangePeriodDTO `json:"periods"`
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
	Symbol      string                       `json:"symbol"`
	NativeQuote string                       `json:"native_quote"`
	AsOf        string                       `json:"as_of"`
	Now         *num6                        `json:"now"`
	Periods     map[string]ftChangePeriodDTO `json:"periods"`
}

// --- rendering ---

func renderFTChange(token string, periods []prices.Period, currencies []string, res apiprices.ChangeResult) ftChangeDTO {
	out := ftChangeDTO{
		Token:      token,
		AsOf:       formatRFC3339(res.AsOf),
		Currencies: make(map[string]ftChangeForCurrencyDTO, len(currencies)),
	}
	for _, cur := range currencies {
		c, ok := res.Currencies[cur]
		if !ok {
			// The service composes an entry for every requested currency,
			// so this branch is defensive — keep the key with all-nulls.
			out.Currencies[cur] = ftChangeForCurrencyDTO{
				Periods: emptyPeriodsMap(periods),
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
			Periods: renderPeriodsMap(periods, c.ByPeriod),
		}
	}
	return out
}

func renderRWAChange(symbol, nativeQuote string, periods []prices.Period, res apiprices.ChangeResult) rwaChangeDTO {
	cur, ok := res.Currencies[nativeQuote]
	if !ok {
		// Defensive — service always emits the requested currency entry.
		return rwaChangeDTO{
			Symbol:      symbol,
			NativeQuote: nativeQuote,
			AsOf:        formatRFC3339(res.AsOf),
			Periods:     emptyPeriodsMap(periods),
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
		AsOf:        formatRFC3339(res.AsOf),
		Now:         nowVal,
		Periods:     renderPeriodsMap(periods, cur.ByPeriod),
	}
}

func renderPeriodsMap(periods []prices.Period, by map[prices.Period]apiprices.ChangeForPeriod) map[string]ftChangePeriodDTO {
	out := make(map[string]ftChangePeriodDTO, len(periods))
	for _, p := range periods {
		out[string(p)] = renderPeriodDTO(by[p])
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

func emptyPeriodsMap(periods []prices.Period) map[string]ftChangePeriodDTO {
	out := make(map[string]ftChangePeriodDTO, len(periods))
	for _, p := range periods {
		out[string(p)] = ftChangePeriodDTO{}
	}
	return out
}

func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
