package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// ChartEnvelope is the top-level metadata block on every chart response.
// Symbol/Currency identify the series; Kind/Interval are self-describing
// so dashboards can branch on response shape without re-parsing the URL.
type ChartEnvelope struct {
	Symbol   string `json:"symbol"`
	Currency string `json:"currency"`
	Kind     string `json:"kind"`     // "series" | "ohlc" | "ohlcv"
	Interval string `json:"interval"` // "raw" | "1m" | "5m" | "15m" | "1h" | "4h" | "1d"
}

// SeriesDTO is the wire shape for /series — one (timestamp, close-price) per bucket.
type SeriesDTO struct {
	ChartEnvelope
	Points []SeriesPointDTO `json:"points"`
}

// SeriesPointDTO is one row of /series. When `?in=usd` is requested the
// converted close price flatness in as a top-level numeric key (3-letter
// ISO code can never collide with the reserved "t" / "p"). Conv is empty
// when no FX conversion was requested or applied.
type SeriesPointDTO struct {
	T    string          `json:"t"` // RFC3339 UTC, e.g. "2026-04-27T12:00:00Z"
	P    num6            `json:"p"`
	Conv map[string]num6 `json:"-"` // marshalled flat via MarshalJSON
}

// MarshalJSON flattens Conv keys onto the top-level object so a client
// reads `point.usd` (number) instead of `point.conv.usd`. Currency codes
// are 3 letters; collision with "t"/"p" is impossible.
func (d SeriesPointDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 2+len(d.Conv))
	out["t"] = d.T
	out["p"] = d.P
	for cur, v := range d.Conv {
		out[cur] = v
	}
	return json.Marshal(out)
}

// OHLCDTO is the wire shape for /ohlc — candles without volume.
type OHLCDTO struct {
	ChartEnvelope
	Candles []CandleDTO `json:"candles"`
}

// CandleDTO is the no-volume candle row. When `?in=usd` is requested the
// converted o/h/l/c plus rate/rate_ts inline as a nested object keyed by
// the target currency (see ADR-0015). Conv is empty when no FX conversion
// was requested or applied.
type CandleDTO struct {
	T    string                        `json:"t"`
	O    num6                          `json:"o"`
	H    num6                          `json:"h"`
	L    num6                          `json:"l"`
	C    num6                          `json:"c"`
	N    int64                         `json:"n"` // samples — fronts can mark partial buckets
	Conv map[string]ConvertedCandleDTO `json:"-"`
}

// ConvertedCandleDTO is one converted candle (close-of-bucket FX; see ADR-0015).
// `rate` is the multiplier applied to all four price fields; `rate_ts` is the
// timestamp of the FX point that produced the rate (auditability for fronts).
type ConvertedCandleDTO struct {
	O      num6   `json:"o"`
	H      num6   `json:"h"`
	L      num6   `json:"l"`
	C      num6   `json:"c"`
	Rate   num6   `json:"rate"`
	RateTS string `json:"rate_ts"`
}

// MarshalJSON flattens Conv keys onto the top-level candle object so a
// client reads `candle.usd.o` instead of `candle.conv.usd.o`. Currency
// codes are 3 letters; collision with reserved keys is impossible.
func (d CandleDTO) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 6+len(d.Conv))
	out["t"] = d.T
	out["o"] = d.O
	out["h"] = d.H
	out["l"] = d.L
	out["c"] = d.C
	out["n"] = d.N
	for cur, v := range d.Conv {
		out[cur] = v
	}
	return json.Marshal(out)
}

// OHLCVDTO is the wire shape for /ohlcv. Defined alongside the rest of the
// chart DTOs (even though the endpoint currently 501s — see ADR-0015) so
// the OpenAPI spec and frontend can build against the final shape before
// volume ingestion lands.
type OHLCVDTO struct {
	ChartEnvelope
	Candles []CandleVolDTO `json:"candles"`
}

// CandleVolDTO is the candle row with traded-volume (base + quote).
type CandleVolDTO struct {
	CandleDTO
	Vb num6 `json:"vb"` // volume in base token
	Vq num6 `json:"vq"` // volume in quote token (or quote-converted-to-?in= when FX applies)
}

// ParseChartInterval reads ?interval= and validates it. Empty raw value is a
// 400 — interval is required (no server-side default), see ADR-0015.
func ParseChartInterval(c *gin.Context) (apiprices.Interval, error) {
	raw := strings.TrimSpace(c.Query("interval"))
	if raw == "" {
		return "", coreerrors.InvalidArgument("'interval' query parameter is required")
	}
	iv, ok := apiprices.ParseInterval(raw)
	if !ok {
		return "", coreerrors.InvalidArgument(
			"Invalid 'interval' parameter: must be one of raw, 1m, 5m, 15m, 1h, 4h, 1d")
	}
	return iv, nil
}

// ParseChartIn reads `?in=<currency>` for chart endpoints. Charts cap the
// list to ≤1 target (see ADR-0015) — multiple targets is a defense-in-depth
// 400 here even though only handlers wire it; per-endpoint MaxIn is
// enforced by ChartService.preflight after this returns.
//
// Empty / missing → nil, no error.
// Converter == nil → 400 with "not enabled on this server".
// Multiple comma-separated values → 400 (cap=1 for charts).
// Unknown currency → 400.
func ParseChartIn(c *gin.Context, converter apiprices.PriceConverter) ([]prices.Currency, error) {
	raw := strings.TrimSpace(c.Query("in"))
	if raw == "" {
		return nil, nil
	}
	if converter == nil {
		return nil, coreerrors.InvalidArgument("'in' parameter is not enabled on this server")
	}
	parts := strings.Split(raw, ",")
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return nil, nil
	}
	if len(nonEmpty) > 1 {
		return nil, coreerrors.InvalidArgument(
			"'in' accepts at most one currency for charts; use multiple requests for several targets")
	}
	cur, err := prices.NewCurrency(nonEmpty[0])
	if err != nil {
		return nil, coreerrors.InvalidArgument("Invalid 'in' currency: " + nonEmpty[0])
	}
	return []prices.Currency{cur}, nil
}

// renderSeriesConv projects ChartService Conv (close-only per target) onto
// the wire DTO. nil-safe — returns nil for empty input so MarshalJSON skips
// the keys cleanly.
func renderSeriesConv(in map[prices.Currency]decimal.Decimal) map[string]num6 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]num6, len(in))
	for k, v := range in {
		out[string(k)] = newNum6(v)
	}
	return out
}

// renderCandleConv projects ChartService ConvertedCandle onto the wire DTO.
// Same nil-safety as renderSeriesConv.
func renderCandleConv(in map[prices.Currency]apiprices.ConvertedCandle) map[string]ConvertedCandleDTO {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ConvertedCandleDTO, len(in))
	for k, v := range in {
		out[string(k)] = ConvertedCandleDTO{
			O:      newNum6(v.Open),
			H:      newNum6(v.High),
			L:      newNum6(v.Low),
			C:      newNum6(v.Close),
			Rate:   newNum6(v.Rate),
			RateTS: v.RateTS.UTC().Format("2006-01-02T15:04:05Z"),
		}
	}
	return out
}

// notImplementedOHLCVCode is the wire-stable error code for the parked
// OHLCV endpoint. Frontend (and contract tests) match against this exact
// string — do not rename without coordinating.
const notImplementedOHLCVCode coreerrors.Code = "OHLCV_NOT_IMPLEMENTED"

// notImplementedOHLCVMessage is the user-visible body text.
const notImplementedOHLCVMessage = "OHLCV is not yet available;"

// NotImplementedOHLCV is the gin handler for /ohlcv until volume
// ingestion ships (see ADR-0015). Returns 501 with a fixed error envelope.
// Registering this stub from day one freezes the URL contract — frontend
// can build against the path while we sequence backend work.
func NotImplementedOHLCV() gin.HandlerFunc {
	return func(c *gin.Context) {
		common.RespondErrorWithStatus(c,
			http.StatusNotImplemented,
			notImplementedOHLCVCode,
			notImplementedOHLCVMessage,
		)
	}
}
