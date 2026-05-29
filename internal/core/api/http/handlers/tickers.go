package handlers

import (
	"context"
	stderrors "errors"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	apitickers "quotes/internal/core/application/tickers"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"
	"quotes/internal/metrics"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// TickerDeps wires the tickers HTTP layer.
//
// Converter is optional — when nil, requests with ?in= return 400 (matches the
// RWA endpoints under the same posture). Source defaults to CoinGecko.
type TickerDeps struct {
	Service          apitickers.QueryService
	Converter        apiprices.PriceConverter
	DefaultSource    prices.Source
	MaxInCurrencies  int
	TickerStaleAfter time.Duration
}

// LatestByToken — GET /v1/tickers/:token/latest
//
// Query params:
//
//	?in=usd,eur,...           — convert price+volume to these currencies (see ADR-0013)
//	?include_stale=true       — return rows older than TickerStaleAfter (default hides them)
//
// Response: TickersSnapshotDTO. 200 on success (even with empty tickers — cold
// start), 404 when token unknown, 400 on bad ?in= or unsupported currency.
func (d TickerDeps) LatestByToken() gin.HandlerFunc {
	type request struct {
		Token        prices.Token
		Source       prices.Source
		IncludeStale bool
		InTargets    []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		tok, err := parseTokenParam(c)
		if err != nil {
			return request{}, err
		}
		inTargets, err := d.parseInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{
			Token:        tok,
			Source:       d.DefaultSource,
			IncludeStale: parseBoolQuery(c, "include_stale", false),
			InTargets:    inTargets,
		}, nil
	}
	action := func(ctx context.Context, req request) (TickersSnapshotDTO, error) {
		snap, err := d.Service.LatestSnapshot(ctx, apitickers.LatestQuery{
			Token:        req.Token,
			Source:       req.Source,
			IncludeStale: req.IncludeStale,
			StaleAfter:   d.TickerStaleAfter,
		})
		if err != nil {
			return TickersSnapshotDTO{}, err
		}
		return d.buildSnapshotDTO(ctx, req.Token, snap, req.InTargets), nil
	}
	return common.Wrap(bind, action)
}

// Distribution — GET /v1/tickers/:token/distribution?group_by=exchange|target&in=usd
//
// Returns one row per group with summed volume and share_pct. Stale rows are
// always excluded server-side regardless of any caller flag — the pie chart is
// about current market structure, not historical noise.
func (d TickerDeps) Distribution() gin.HandlerFunc {
	type request struct {
		Token     prices.Token
		Source    prices.Source
		GroupBy   tickers.GroupBy
		InTargets []prices.Currency
	}
	bind := func(c *gin.Context) (request, error) {
		tok, err := parseTokenParam(c)
		if err != nil {
			return request{}, err
		}
		gb, err := tickers.NewGroupBy(c.Query("group_by"))
		if err != nil {
			return request{}, coreerrors.InvalidArgument(err.Error())
		}
		inTargets, err := d.parseInQuery(c)
		if err != nil {
			return request{}, err
		}
		return request{
			Token:     tok,
			Source:    d.DefaultSource,
			GroupBy:   gb,
			InTargets: inTargets,
		}, nil
	}
	action := func(ctx context.Context, req request) (DistributionDTO, error) {
		dist, err := d.Service.VolumeDistribution(ctx, apitickers.DistributionQuery{
			Token:      req.Token,
			Source:     req.Source,
			GroupBy:    req.GroupBy,
			StaleAfter: d.TickerStaleAfter,
		})
		if err != nil {
			return DistributionDTO{}, err
		}
		return d.buildDistributionDTO(ctx, req.Token, dist, req.InTargets), nil
	}
	return common.Wrap(bind, action)
}

// --- bind helpers ---

func parseTokenParam(c *gin.Context) (prices.Token, error) {
	raw := strings.TrimSpace(c.Param("token"))
	if raw == "" {
		return "", coreerrors.InvalidArgument("token path parameter is required")
	}
	tok, err := prices.NewToken(raw)
	if err != nil {
		return "", coreerrors.NotFound("Token not found")
	}
	return tok, nil
}

func parseBoolQuery(c *gin.Context, key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(c.Query(key)))
	switch v {
	case "", "0", "false", "no":
		return def && v == ""
	case "1", "true", "yes":
		return true
	default:
		return def
	}
}

// parseInQuery mirrors the RWA pattern (rwa_prices.go). Empty returns nil.
// Unknown values or > MaxInCurrencies → 400.
func (d TickerDeps) parseInQuery(c *gin.Context) ([]prices.Currency, error) {
	raw := strings.TrimSpace(c.Query("in"))
	if raw == "" {
		return nil, nil
	}
	if d.Converter == nil {
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

// --- DTOs ---

// TickersSnapshotDTO is the on-wire shape for /v1/tickers/:token/latest.
type TickersSnapshotDTO struct {
	Source    string         `json:"source"`
	Entity    string         `json:"entity"`
	Timestamp string         `json:"timestamp"`
	Tickers   []TickerRowDTO `json:"tickers"`
}

// TickerRowDTO is one (exchange, target) row.
type TickerRowDTO struct {
	Exchange        string                          `json:"exchange"` // identifier (lookup key)
	ExchangeName    string                          `json:"exchange_name"`
	ExchangeKind    string                          `json:"exchange_kind"` // "cex" / "dex"
	LogoURL         string                          `json:"logo_url,omitempty"`
	Target          string                          `json:"target"`
	Pair            string                          `json:"pair"`                      // "MVRK/BTC"
	LastPrice       string                          `json:"last_price"`                // native; in `target` units
	VolumeBase      *string                         `json:"volume_24h_base,omitempty"` // in token units
	Change24hPct    *string                         `json:"change_24h_pct"`            // null when no 24h-ago row
	BidAskSpreadPct *string                         `json:"bid_ask_spread_pct,omitempty"`
	TrustScore      string                          `json:"trust_score,omitempty"`
	IsStale         bool                            `json:"is_stale"`
	IsAnomaly       bool                            `json:"is_anomaly"`
	TradeURL        string                          `json:"trade_url,omitempty"`
	In              map[string]ConvertedTickerBlock `json:"in,omitempty"`
}

// ConvertedTickerBlock carries the per-`?in=` projection for one row.
//
// Either Price+Volume are present (success) or FX.Error is populated and the
// numeric fields are omitted (per-target failure, parent response stays 200).
type ConvertedTickerBlock struct {
	Price     string  `json:"price,omitempty"`
	Volume24h *string `json:"volume_24h,omitempty"`
	FX        FXMeta  `json:"fx"`
}

// FXMeta — same shape used by RWA `?in=` responses for cross-route consistency.
type FXMeta struct {
	Rate   string `json:"rate,omitempty"`
	Source string `json:"source,omitempty"`
	TS     string `json:"ts,omitempty"`
	Method string `json:"method,omitempty"` // "rate" | "identity"
	Stale  bool   `json:"stale,omitempty"`
	Error  string `json:"error,omitempty"` // "no_rate" | "unsupported_target" | "unregistered_source"
}

// DistributionDTO is the on-wire shape for /v1/tickers/:token/distribution.
type DistributionDTO struct {
	Source          string               `json:"source"`
	Entity          string               `json:"entity"`
	Timestamp       string               `json:"timestamp"`
	GroupBy         string               `json:"group_by"`
	TotalVolumeBase string               `json:"total_volume_base"`
	Rows            []DistributionRowDTO `json:"rows"`
}

// DistributionRowDTO is one slice of the pie.
type DistributionRowDTO struct {
	// Exchange grouping fields (group_by=exchange).
	Exchange     string `json:"exchange,omitempty"`
	ExchangeName string `json:"exchange_name,omitempty"`
	LogoURL      string `json:"logo_url,omitempty"`
	ExchangeKind string `json:"exchange_kind,omitempty"`
	// Target grouping field (group_by=target).
	Target string `json:"target,omitempty"`

	VolumeBase string                          `json:"volume_24h_base"`
	SharePct   string                          `json:"share_pct"`
	In         map[string]ConvertedVolumeBlock `json:"in,omitempty"`
}

// ConvertedVolumeBlock is a slimmer version of ConvertedTickerBlock — pie rows
// only convert volume, never a price.
type ConvertedVolumeBlock struct {
	Volume24h string `json:"volume_24h,omitempty"`
	FX        FXMeta `json:"fx"`
}

// --- DTO builders ---

func (d TickerDeps) buildSnapshotDTO(
	ctx context.Context,
	token prices.Token,
	snap tickers.Snapshot,
	inTargets []prices.Currency,
) TickersSnapshotDTO {
	out := TickersSnapshotDTO{
		Source:    string(snap.Source),
		Entity:    string(snap.Token),
		Timestamp: formatTime(snap.Timestamp),
		Tickers:   make([]TickerRowDTO, 0, len(snap.Rows)),
	}
	if snap.Token == "" {
		out.Entity = string(token)
	}
	for _, r := range snap.Rows {
		row := TickerRowDTO{
			Exchange:        r.Exchange.ID,
			ExchangeName:    r.Exchange.Name,
			ExchangeKind:    string(r.Exchange.Kind),
			LogoURL:         r.Exchange.LogoURL,
			Target:          r.TargetSymbol,
			Pair:            strings.ToUpper(string(token)) + "/" + strings.ToUpper(r.TargetSymbol),
			LastPrice:       formatDec(r.LastPrice),
			VolumeBase:      optDec(r.VolumeBase),
			Change24hPct:    optDec(r.Change24hPct),
			BidAskSpreadPct: optDec(r.BidAskSpread),
			TrustScore:      r.TrustScore,
			IsStale:         r.IsStale,
			IsAnomaly:       r.IsAnomaly,
			TradeURL:        r.TradeURL,
		}
		if len(inTargets) > 0 {
			row.In = d.convertRowTargets(ctx, token, r, inTargets)
		}
		out.Tickers = append(out.Tickers, row)
	}
	return out
}

func (d TickerDeps) buildDistributionDTO(
	ctx context.Context,
	token prices.Token,
	dist tickers.Distribution,
	inTargets []prices.Currency,
) DistributionDTO {
	out := DistributionDTO{
		Source:          string(dist.Source),
		Entity:          string(dist.Token),
		Timestamp:       formatTime(dist.Timestamp),
		GroupBy:         string(dist.GroupBy),
		TotalVolumeBase: formatDec(dist.Total),
		Rows:            make([]DistributionRowDTO, 0, len(dist.Rows)),
	}
	if dist.Token == "" {
		out.Entity = string(token)
	}
	for _, r := range dist.Rows {
		row := DistributionRowDTO{
			VolumeBase: formatDec(r.VolumeBase),
			SharePct:   formatDec(r.SharePct),
		}
		if dist.GroupBy == tickers.GroupByExchange {
			row.Exchange = r.Exchange.ID
			row.ExchangeName = r.Exchange.Name
			row.LogoURL = r.Exchange.LogoURL
			row.ExchangeKind = string(r.Exchange.Kind)
		} else {
			row.Target = r.TargetSymbol
		}
		if len(inTargets) > 0 {
			row.In = d.convertGroupVolumeTargets(ctx, token, r.VolumeBase, dist.Timestamp, inTargets)
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// convertRowTargets builds the in.<target> map for one ticker row. The price
// converts from `target_symbol` → currency (FX(target → currency) × last_price);
// the volume converts from `token` → currency (FX(token → currency) × volume).
// Per-target failures populate fx.error and omit the numeric fields. Parent
// response stays 200.
func (d TickerDeps) convertRowTargets(
	ctx context.Context,
	token prices.Token,
	row tickers.SnapshotRow,
	targets []prices.Currency,
) map[string]ConvertedTickerBlock {
	if d.Converter == nil {
		return nil
	}
	out := make(map[string]ConvertedTickerBlock, len(targets))
	// Resolve target_symbol → Token (so we can use it as a converter source).
	targetTok, targetOK := promoteTickerTargetToken(row.TargetSymbol)

	for _, cur := range targets {
		block := ConvertedTickerBlock{}
		// Price: target_symbol → cur.
		if !targetOK {
			block.FX = FXMeta{Error: "unregistered_source"}
			metrics.FXConversionsTotal.WithLabelValues(row.TargetSymbol, string(cur), "unregistered_source").Inc()
			out[string(cur)] = block
			continue
		}
		priceRes, priceErr := timedConvert(ctx, d.Converter, targetTok, cur, row.LastPrice, row.Timestamp)
		if priceErr != nil {
			block.FX = FXMeta{Error: errToFXLabel(priceErr)}
			out[string(cur)] = block
			continue
		}
		block.Price = formatDec(priceRes.Amount)
		block.FX = FXMeta{
			Rate:   formatDec(priceRes.Rate),
			Source: string(priceRes.Source),
			TS:     formatTime(priceRes.RateTS),
			Method: fxMethod(priceRes),
			Stale:  priceRes.Stale,
		}
		if priceRes.Stale {
			metrics.FXStaleResponsesTotal.WithLabelValues(string(cur)).Inc()
		}

		// Volume: token → cur. We use the token (e.g. mvrk) as the source so
		// the converter looks up FX(mvrk → currency) — works whether the row
		// is MVRK/BTC or MVRK/USDT.
		if row.VolumeBase != nil {
			volRes, volErr := timedConvert(ctx, d.Converter, token, cur, *row.VolumeBase, row.Timestamp)
			if volErr == nil {
				s := formatDec(volRes.Amount)
				block.Volume24h = &s
				if volRes.Stale {
					metrics.FXStaleResponsesTotal.WithLabelValues(string(cur)).Inc()
				}
			}
			// volume failure is silent — leave Volume24h nil but keep price block.
		}
		out[string(cur)] = block
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d TickerDeps) convertGroupVolumeTargets(
	ctx context.Context,
	token prices.Token,
	volumeBase decimal.Decimal,
	ts time.Time,
	targets []prices.Currency,
) map[string]ConvertedVolumeBlock {
	if d.Converter == nil {
		return nil
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	out := make(map[string]ConvertedVolumeBlock, len(targets))
	for _, cur := range targets {
		res, err := timedConvert(ctx, d.Converter, token, cur, volumeBase, ts)
		if err != nil {
			out[string(cur)] = ConvertedVolumeBlock{FX: FXMeta{Error: errToFXLabel(err)}}
			continue
		}
		block := ConvertedVolumeBlock{
			Volume24h: formatDec(res.Amount),
			FX: FXMeta{
				Rate:   formatDec(res.Rate),
				Source: string(res.Source),
				TS:     formatTime(res.RateTS),
				Method: fxMethod(res),
				Stale:  res.Stale,
			},
		}
		if res.Stale {
			metrics.FXStaleResponsesTotal.WithLabelValues(string(cur)).Inc()
		}
		out[string(cur)] = block
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// --- formatting + lookups ---

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func formatDec(d decimal.Decimal) string {
	return d.String()
}

func optDec(p *decimal.Decimal) *string {
	if p == nil {
		return nil
	}
	s := p.String()
	return &s
}

// promoteTickerTargetToken upgrades the ticker target_symbol string to a
// registered Token (so PriceConverter.Convert can use it as a source). Returns
// (zero, false) when the symbol isn't in the registry — handler drops that
// row's converted block with fx.error="unregistered_source".
func promoteTickerTargetToken(target string) (prices.Token, bool) {
	tok, err := prices.NewToken(target)
	if err != nil {
		return "", false
	}
	return tok, true
}

func errToFXLabel(err error) string {
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

func fxMethod(r apiprices.ConversionResult) string {
	if r.Identity {
		return "identity"
	}
	return "rate"
}
