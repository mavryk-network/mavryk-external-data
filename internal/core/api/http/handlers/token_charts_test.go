package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// stubCandleRepo is a CandleRepository test double. Captures the last query
// for assertions and returns canned candles or err.
type stubCandleRepo struct {
	mu      sync.Mutex // guards mutable fields for concurrent-handler tests (overview)
	candles []apiprices.Candle
	err     error
	seen    apiprices.CandleQuery
}

func (s *stubCandleRepo) QueryCandles(_ context.Context, q apiprices.CandleQuery) ([]apiprices.Candle, error) {
	s.mu.Lock()
	s.seen = q
	candles, err := s.candles, s.err
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]apiprices.Candle, len(candles))
	copy(out, candles)
	return out, nil
}

func newTokenChartEngine(t *testing.T, repo *stubCandleRepo) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	deps := TokenChartDeps{
		Charts: &apiprices.ChartService{
			Repo:     repo,
			Caps:     apiprices.DefaultCaps(),
			MaxLimit: 1000,
		},
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      1000,
		DefaultLimit:  100,
	}
	r.GET("/v1/prices/:token/series", deps.Series())
	r.GET("/v1/prices/:token/ohlc", deps.OHLC())
	r.GET("/v1/prices/:token/ohlcv", NotImplementedOHLCV())
	return r
}

func decFA(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Decode-side mirrors of the wire DTOs. num6 is output-only (MarshalJSON
// canonicalises decimals; no UnmarshalJSON) — tests use decimal.Decimal,
// which accepts JSON numbers and strings alike.
type seriesDTODecoded struct {
	Symbol   string `json:"symbol"`
	Currency string `json:"currency"`
	Kind     string `json:"kind"`
	Interval string `json:"interval"`
	Points   []struct {
		T string          `json:"t"`
		P decimal.Decimal `json:"p"`
	} `json:"points"`
}

type ohlcDTODecoded struct {
	Symbol   string `json:"symbol"`
	Currency string `json:"currency"`
	Kind     string `json:"kind"`
	Interval string `json:"interval"`
	Candles  []struct {
		T string          `json:"t"`
		O decimal.Decimal `json:"o"`
		H decimal.Decimal `json:"h"`
		L decimal.Decimal `json:"l"`
		C decimal.Decimal `json:"c"`
		N int64           `json:"n"`
	} `json:"candles"`
}

// --- /series ---

func TestSeries_OK_ReturnsClosePoints(t *testing.T) {
	registerTestTokens(t)
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("1.0"), High: decFA("2.0"), Low: decFA("0.5"), Close: decFA("1.5"), Samples: 12},
			{Bucket: bk.Add(time.Hour), Open: decFA("1.5"), High: decFA("3.0"), Low: decFA("1.0"), Close: decFA("2.5"), Samples: 30},
		},
	}
	r := newTokenChartEngine(t, repo)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/prices/mvrk/series?interval=1h&currency=usd&from=2026-05-01T00:00:00Z&to=2026-05-01T23:59:59Z",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got seriesDTODecoded
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, w.Body.String())
	}
	if got.Symbol != "mvrk" || got.Currency != "usd" || got.Kind != "series" || got.Interval != "1h" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(got.Points))
	}
	if got.Points[0].T != "2026-05-01T12:00:00Z" {
		t.Errorf("Points[0].T = %q", got.Points[0].T)
	}
	if !got.Points[0].P.Equal(decFA("1.5")) {
		t.Errorf("Points[0].P = %s, want 1.5", got.Points[0].P)
	}
	if !got.Points[1].P.Equal(decFA("2.5")) {
		t.Errorf("Points[1].P = %s, want 2.5", got.Points[1].P)
	}
	// Verify repo received the resolved AuxKey.
	if repo.seen.AuxKey != "coingecko|usd" {
		t.Errorf("AuxKey = %q, want coingecko|usd", repo.seen.AuxKey)
	}
	if repo.seen.Interval != apiprices.Interval1h {
		t.Errorf("Interval = %q", repo.seen.Interval)
	}
}

func TestSeries_MissingInterval_400(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/series?currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSeries_BadInterval_400(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/series?interval=2m&currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSeries_RawInterval_400_Stage1(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/series?interval=raw&currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (raw not yet wired for FA charts)", w.Code)
	}
}

func TestSeries_MissingCurrency_400(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/series?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSeries_BadCurrency_400(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/series?interval=1h&currency=zzz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSeries_UnknownToken_404(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet, "/v1/prices/notatoken/series?interval=1h&currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestSeries_RangeOverCap_416(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	// 1m has a 7-day cap (see ADR-0015). The handler maps cap-exceeded
	// to 416 RANGE_NOT_SATISFIABLE — distinct from 400 so clients can
	// render "narrow your range" UX without parsing the error message.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/prices/mvrk/series?interval=1m&currency=usd&from=2025-01-01T00:00:00Z&to=2025-02-01T00:00:00Z",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want 416", w.Code)
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "RANGE_NOT_SATISFIABLE" {
		t.Errorf("code = %q, want RANGE_NOT_SATISFIABLE", body.Code)
	}
}

func TestSeries_FullGranularitiesAccepted(t *testing.T) {
	// 5m / 15m / 4h are served via repository re-bucket — handler must let
	// them through preflight. Actual bucket SQL is verified in the
	// integration suite (tests/integration/token_charts_integration_test.go).
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	for _, iv := range []string{"5m", "15m", "4h"} {
		t.Run("interval="+iv, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet,
				"/v1/prices/mvrk/series?interval="+iv+"&currency=usd", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 (interval=%s)", w.Code, iv)
			}
		})
	}
}

// --- /ohlc ---

func TestOHLC_OK_ReturnsCandles(t *testing.T) {
	registerTestTokens(t)
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("100.50"), High: decFA("101.20"), Low: decFA("100.00"), Close: decFA("100.80"), Samples: 30},
		},
	}
	r := newTokenChartEngine(t, repo)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/prices/mvrk/ohlc?interval=1h&currency=usd",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got ohlcDTODecoded
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != "ohlc" || got.Interval != "1h" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Candles) != 1 {
		t.Fatalf("candles = %d, want 1", len(got.Candles))
	}
	c := got.Candles[0]
	if c.T != "2026-05-01T12:00:00Z" {
		t.Errorf("T = %q", c.T)
	}
	if c.N != 30 {
		t.Errorf("N = %d, want 30", c.N)
	}
	if !c.O.Equal(decFA("100.5")) {
		t.Errorf("O = %s, want 100.5", c.O)
	}
	if !c.C.Equal(decFA("100.8")) {
		t.Errorf("C = %s, want 100.8", c.C)
	}
}

// --- /ohlcv: contract test for the parked TODO ---

func TestOHLCV_StubReturns501WithFixedBody(t *testing.T) {
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/ohlcv?interval=1h&currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, w.Body.String())
	}
	if body.Code != "OHLCV_NOT_IMPLEMENTED" {
		t.Errorf("code = %q, want OHLCV_NOT_IMPLEMENTED", body.Code)
	}
	if body.Message == "" {
		t.Errorf("empty message")
	}
}

func TestOHLCV_StubIgnoresQueryParams(t *testing.T) {
	// 501 is independent of params — the stub must not accidentally
	// fall through to validation. Sends garbage; expect the same 501.
	registerTestTokens(t)
	r := newTokenChartEngine(t, &stubCandleRepo{})
	req := httptest.NewRequest(http.MethodGet,
		"/v1/prices/notatoken/ohlcv?interval=garbage&currency=zzz",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 (regardless of params)", w.Code)
	}
}
