package handlers

import (
	"context"
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

// PairLookup is the read-only contract handlers need to resolve a pair
// `{base}-{quote}` symbol into the canonical RWAPair (used for both the
// price query EntityKey and the `?in=` quote-token resolution).
type PairLookup interface {
	LookupRWAPairBySymbol(ctx context.Context, base, quote string) (prices.RWAPair, error)
}

// RWAPriceDeps wires the RWA-side dependencies.
type RWAPriceDeps struct {
	Service       apiprices.QueryService
	Lookup        PairLookup       // required: resolves `{base}-{quote}` -> RWAPair
	Converter     apiprices.PriceConverter // optional; when nil, `?in=` returns 400
	DefaultSource prices.Source            // typically prices.SourceEquiteez
	MaxLimit      int
	DefaultLimit  int
	// MaxInCurrencies caps the number of comma-separated currencies in `?in=`.
	// 0 means unlimited (not recommended); production should pin to e.g. 10.
	MaxInCurrencies int
}

// ListBySymbol — GET /v1/rwa/:symbol
//
// `:symbol` is `{base}-{quote}` (case-insensitive), e.g. `mars1-usdt`.
// Optional `?in=usd,eur` adds a per-row `in` map with converted prices.
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
		inTargets, err := d.parseInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{
			Pair: pair,
			Query: prices.Query{
				Source:    d.DefaultSource,
				EntityKey: pair.EntityKey(),
				Metrics:   pq.Metrics,
				From:      pq.From,
				To:        pq.To,
				Limit:     pq.Limit,
			},
			InTargets: inTargets,
		}, nil
	}
	type pointDTO struct {
		Timestamp string                           `json:"timestamp"`
		Side      string                           `json:"side"`
		Price     decimal.Decimal                  `json:"price"`
		Size      *decimal.Decimal                 `json:"size,omitempty"`
		In        map[string]convertedListEntryDTO `json:"in,omitempty"`
	}
	action := func(ctx context.Context, req request) ([]pointDTO, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return nil, err
		}
		var quoteToken prices.Token
		var quoteResolved bool
		if len(req.InTargets) > 0 {
			quoteToken, quoteResolved = promoteQuoteToken(req.Pair.QuoteSymbol)
		}
		out := make([]pointDTO, len(points))
		for i, p := range points {
			row := pointDTO{
				Timestamp: p.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				Side:      p.Metric,
				Price:     p.Price,
				Size:      p.Size,
			}
			if len(req.InTargets) > 0 {
				row.In = d.convertOnePoint(ctx, quoteToken, quoteResolved, req.InTargets, p.Price, p.Timestamp)
			}
			out[i] = row
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// LatestBySymbol — GET /v1/rwa/:symbol/latest
//
// Optional `?in=usd,eur,aed` adds a transposed `in` block per currency.
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
			},
			InTargets: inTargets,
		}, nil
	}
	action := func(ctx context.Context, req request) (snapshotDTO, error) {
		points, err := d.Service.Query(ctx, req.Query)
		if err != nil {
			return snapshotDTO{}, err
		}
		snap, ok := prices.LatestSnapshot(points)
		if !ok {
			return snapshotDTO{}, coreerrors.NotFound("No prices for pair")
		}
		dto := snapshotDTO{
			Source:    string(snap.Source),
			Entity:    snap.EntityKey,
			Timestamp: snap.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			Values:    snap.Values,
		}
		if len(req.InTargets) == 0 {
			return dto, nil
		}
		quoteToken, quoteResolved := promoteQuoteToken(req.Pair.QuoteSymbol)
		if quoteResolved {
			dto.NativeQuote = string(quoteToken)
		}
		dto.In = make(map[string]convertedSnapshotDTO, len(req.InTargets))
		for _, target := range req.InTargets {
			block := d.convertSnapshotBlock(ctx, quoteToken, quoteResolved, target, snap.Values, snap.Timestamp)
			dto.In[string(target)] = block
		}
		return dto, nil
	}
	return common.Wrap(bind, action)
}

// --- Response DTOs (handler-local) ---

type snapshotDTO struct {
	Source      string                          `json:"source"`
	Entity      string                          `json:"entity"`
	Timestamp   string                          `json:"timestamp"`
	NativeQuote string                          `json:"native_quote,omitempty"`
	Values      map[string]decimal.Decimal      `json:"values"`
	In          map[string]convertedSnapshotDTO `json:"in,omitempty"`
}

type convertedSnapshotDTO struct {
	Values map[string]decimal.Decimal `json:"values,omitempty"`
	FX     fxMetaDTO                  `json:"fx"`
}

type convertedListEntryDTO struct {
	Price *decimal.Decimal `json:"price,omitempty"`
	FX    fxMetaDTO        `json:"fx"`
}

type fxMetaDTO struct {
	Rate    *decimal.Decimal `json:"rate,omitempty"`
	Source  string           `json:"source,omitempty"`
	TS      string           `json:"ts,omitempty"`
	Method  string           `json:"method,omitempty"` // "rate" | "identity"
	Stale   bool             `json:"stale,omitempty"`
	Warning string           `json:"warning,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// --- internal helpers ---

// resolvePairFromPath parses `:symbol` and resolves it to a single enabled
// RWAPair. Returns:
//   - 400 INVALID_ARGUMENT — symbol does not parse as `{base}-{quote}`.
//   - 404 NOT_FOUND        — no enabled pair matches (via ErrPairNotFound).
//   - 409 CONFLICT         — 2+ enabled pairs match (via PairAmbiguousError;
//     mapped to coreerrors.Conflict in common.mapDomainError, with pair_ids
//     surfaced in the response details).
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

// promoteQuoteToken tries to lift the pair's quote_symbol into a registered
// Token. Returns (zero, false) when the symbol is unregistered — handler
// renders this as `fx.error: "quote_currency_not_in_registry"` per `?in=`
// target rather than failing the whole response.
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

// convertSnapshotBlock builds one `in.<currency>` block for /latest.
func (d RWAPriceDeps) convertSnapshotBlock(
	ctx context.Context,
	quoteToken prices.Token,
	quoteResolved bool,
	target prices.Currency,
	nativeValues map[string]decimal.Decimal,
	ts time.Time,
) convertedSnapshotDTO {
	block := convertedSnapshotDTO{}
	if !quoteResolved {
		block.FX.Error = "quote_currency_not_in_registry"
		metrics.FXConversionsTotal.WithLabelValues("unknown", string(target), "unregistered_source").Inc()
		return block
	}

	values := make(map[string]decimal.Decimal, len(nativeValues))
	var (
		firstFX  *fxMetaDTO
		anyOK    bool
		firstErr string
		anyStale bool
	)
	for side, native := range nativeValues {
		res, err := timedConvert(ctx, d.Converter, quoteToken, target, native, ts)
		if err != nil {
			if firstErr == "" {
				firstErr = errToFXReason(err)
			}
			continue
		}
		values[side] = res.Amount
		anyOK = true
		if res.Stale {
			anyStale = true
		}
		if firstFX == nil {
			firstFX = fxMetaFromResult(res)
		}
	}
	if anyOK {
		block.Values = values
	}
	if firstFX != nil {
		block.FX = *firstFX
	}
	if !anyOK && firstErr != "" {
		block.FX.Error = firstErr
	}
	if anyStale {
		block.FX.Stale = true
		metrics.FXStaleResponsesTotal.WithLabelValues(string(target)).Inc()
	}
	return block
}

// convertOnePoint builds the `in` map for one row in the list response.
func (d RWAPriceDeps) convertOnePoint(
	ctx context.Context,
	quoteToken prices.Token,
	quoteResolved bool,
	targets []prices.Currency,
	native decimal.Decimal,
	ts time.Time,
) map[string]convertedListEntryDTO {
	out := make(map[string]convertedListEntryDTO, len(targets))
	for _, target := range targets {
		entry := convertedListEntryDTO{}
		if !quoteResolved {
			entry.FX.Error = "quote_currency_not_in_registry"
			metrics.FXConversionsTotal.WithLabelValues("unknown", string(target), "unregistered_source").Inc()
			out[string(target)] = entry
			continue
		}
		res, err := timedConvert(ctx, d.Converter, quoteToken, target, native, ts)
		if err != nil {
			entry.FX.Error = errToFXReason(err)
			out[string(target)] = entry
			continue
		}
		amount := res.Amount
		entry.Price = &amount
		entry.FX = *fxMetaFromResult(res)
		if res.Stale {
			metrics.FXStaleResponsesTotal.WithLabelValues(string(target)).Inc()
		}
		out[string(target)] = entry
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

func fxMetaFromResult(r apiprices.ConversionResult) *fxMetaDTO {
	rate := r.Rate
	method := "rate"
	if r.Identity {
		method = "identity"
	}
	return &fxMetaDTO{
		Rate:   &rate,
		Source: string(r.Source),
		TS:     r.RateTS.UTC().Format("2006-01-02T15:04:05Z"),
		Method: method,
		Stale:  r.Stale,
	}
}

func errToFXReason(err error) string {
	switch {
	case stderrors.Is(err, apiprices.ErrNoFXRate):
		return "no_fx_rate"
	case stderrors.Is(err, apiprices.ErrUnsupportedTargetCurrency):
		return "unsupported_target"
	case stderrors.Is(err, apiprices.ErrSourceTokenNotRegistered):
		return "quote_currency_not_in_registry"
	default:
		return "internal"
	}
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
