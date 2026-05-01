package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// stubChartLookup satisfies handlers.PairLookup. Keeps the same shape as
// stubLookup in rwa_prices_test.go but split out so chart tests can fail
// independently.
type stubChartLookup struct {
	pair prices.RWAPair
	err  error
}

func (s *stubChartLookup) LookupRWAPairBySymbol(_ context.Context, _, _ string) (prices.RWAPair, error) {
	if s.err != nil {
		return prices.RWAPair{}, s.err
	}
	return s.pair, nil
}

func newRWAChartEngine(t *testing.T, repo *stubCandleRepo, lookup PairLookup) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	deps := RWAChartDeps{
		Charts: &apiprices.ChartService{
			Repo:     repo,
			Caps:     apiprices.DefaultCaps(),
			MaxLimit: 1000,
		},
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      1000,
		DefaultLimit:  100,
	}
	r.GET("/v1/rwa/:symbol/series", deps.Series())
	r.GET("/v1/rwa/:symbol/ohlc", deps.OHLC())
	r.GET("/v1/rwa/:symbol/ohlcv", NotImplementedOHLCV())
	return r
}

func samplePair() prices.RWAPair {
	return prices.RWAPair{
		ID:            42,
		Source:        prices.SourceEquiteez,
		BaseSymbol:    "mars1",
		QuoteSymbol:   "USDT",
		OrderbookAddr: "KT1xxx",
		Enabled:       true,
	}
}

// --- /series ---

func TestRWASeries_OK_ReturnsClosePoints(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("100.0"), High: decFA("102.0"), Low: decFA("99.5"), Close: decFA("101.5"), Samples: 12},
			{Bucket: bk.Add(time.Hour), Open: decFA("101.5"), High: decFA("103.0"), Low: decFA("100.0"), Close: decFA("102.5"), Samples: 30},
		},
	}
	lookup := &stubChartLookup{pair: samplePair()}
	r := newRWAChartEngine(t, repo, lookup)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/rwa/mars1-usdt/series?interval=1h&from=2026-05-01T00:00:00Z&to=2026-05-01T23:59:59Z",
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
	if got.Symbol != "mars1-usdt" || got.Currency != "usdt" || got.Kind != "series" || got.Interval != "1h" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(got.Points))
	}
	if !got.Points[0].P.Equal(decFA("101.5")) {
		t.Errorf("Points[0].P = %s", got.Points[0].P)
	}
	// Verify repo received the resolved EntityKey + AuxKey.
	if repo.seen.EntityKey != "42" {
		t.Errorf("EntityKey = %q, want 42", repo.seen.EntityKey)
	}
	if repo.seen.AuxKey != "last" {
		t.Errorf("AuxKey = %q, want last", repo.seen.AuxKey)
	}
}

func TestRWASeries_BadSymbol_400(t *testing.T) {
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{pair: samplePair()})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/justmars1/series?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRWASeries_PairNotFound_404(t *testing.T) {
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{err: prices.ErrPairNotFound})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/notapair-usdt/series?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestRWASeries_AmbiguousSymbol_409(t *testing.T) {
	// Re-use the same PairAmbiguousError shape as the live ListBySymbol
	// test — guarantees the chart handler maps it identically (via the
	// shared mapDomainError in common.Wrap).
	ambig := &prices.PairAmbiguousError{Base: "mars1", Quote: "usdt", IDs: []int64{42, 77}}
	if !errors.Is(ambig, ambig) {
		t.Skip("PairAmbiguousError shape changed")
	}
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{err: ambig})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/series?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	// Body should expose pair_ids per the shared error envelope.
	var body struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err == nil {
		if body.Code != "CONFLICT" {
			t.Errorf("code = %q, want CONFLICT", body.Code)
		}
	}
}

func TestRWASeries_BadInterval_400(t *testing.T) {
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{pair: samplePair()})
	cases := []string{"", "2m", "raw", "garbage"}
	for _, iv := range cases {
		t.Run("interval="+iv, func(t *testing.T) {
			url := "/v1/rwa/mars1-usdt/series"
			if iv != "" {
				url += "?interval=" + iv
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (interval=%q)", w.Code, iv)
			}
		})
	}
}

func TestRWASeries_RangeOverCap_416(t *testing.T) {
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{pair: samplePair()})
	// 1m interval has a 7-day cap (charts.md §2.2). Stage 3 maps the
	// overflow to 416 RANGE_NOT_SATISFIABLE; bad windows (`to < from`)
	// still get 400 INVALID_ARGUMENT.
	req := httptest.NewRequest(http.MethodGet,
		"/v1/rwa/mars1-usdt/series?interval=1m&from=2025-01-01T00:00:00Z&to=2025-02-01T00:00:00Z",
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

// --- /ohlc ---

func TestRWAOHLC_OK_ReturnsCandles(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("100.50"), High: decFA("101.20"), Low: decFA("100.00"), Close: decFA("100.80"), Samples: 30},
		},
	}
	r := newRWAChartEngine(t, repo, &stubChartLookup{pair: samplePair()})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/ohlc?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got ohlcDTODecoded
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Symbol != "mars1-usdt" || got.Kind != "ohlc" || got.Interval != "1h" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Candles) != 1 {
		t.Fatalf("candles = %d, want 1", len(got.Candles))
	}
	c := got.Candles[0]
	if c.T != "2026-05-01T12:00:00Z" {
		t.Errorf("T = %q", c.T)
	}
	if !c.O.Equal(decFA("100.5")) {
		t.Errorf("O = %s, want 100.5", c.O)
	}
	if c.N != 30 {
		t.Errorf("N = %d, want 30", c.N)
	}
}

// --- /ohlcv: shared 501 stub ---

func TestRWAOHLCV_StubReturns501WithFixedBody(t *testing.T) {
	r := newRWAChartEngine(t, &stubCandleRepo{}, &stubChartLookup{pair: samplePair()})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/ohlcv?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Code != "OHLCV_NOT_IMPLEMENTED" {
		t.Errorf("code = %q, want OHLCV_NOT_IMPLEMENTED", body.Code)
	}
	if body.Message == "" {
		t.Errorf("empty message")
	}
}

// --- Symbol case-insensitivity preserved on the wire ---

func TestRWASeries_SymbolLowercasedOnWire(t *testing.T) {
	repo := &stubCandleRepo{}
	r := newRWAChartEngine(t, repo, &stubChartLookup{pair: samplePair()})
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/MARS1-USDT/series?interval=1h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got seriesDTODecoded
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.Symbol != "mars1-usdt" {
		t.Errorf("symbol = %q, want mars1-usdt (lowercased)", got.Symbol)
	}
}

// dummy use of decimal so the import survives `goimports`.
var _ = decimal.NewFromInt(0)

// --- ?in= close-of-bucket FX (Stage 3) ---

// newRWAChartEngineFX wires a converter into ChartService. Mirrors
// newRWAChartEngine but used only by ?in= tests since most tests pass
// nil and don't care about the FX field.
func newRWAChartEngineFX(t *testing.T, repo *stubCandleRepo, lookup PairLookup, conv apiprices.PriceConverter) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	deps := RWAChartDeps{
		Charts: &apiprices.ChartService{
			Repo:      repo,
			Converter: conv,
			Caps:      apiprices.DefaultCaps(),
			MaxLimit:  1000,
			Kind:      "rwa",
		},
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      1000,
		DefaultLimit:  100,
	}
	r.GET("/v1/rwa/:symbol/series", deps.Series())
	r.GET("/v1/rwa/:symbol/ohlc", deps.OHLC())
	return r
}

// pairWithRegisteredQuote returns a pair whose QuoteSymbol resolves through
// prices.NewToken — required for FX since SourceToken must be in the
// runtime token registry. The seed in token_prices_test.go registers mvrk
// and usdt; we use usdt here.
func pairWithRegisteredQuote() prices.RWAPair {
	registerTestTokens(&testing.T{}) // ensure mvrk+usdt are registered
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Enabled: true, CoinGeckoID: "tether"},
	})
	p := samplePair()
	p.QuoteSymbol = "USDT"
	return p
}

func TestRWAOHLC_InUSD_AppliesCloseOfBucketFX(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("100"), High: decFA("110"), Low: decFA("90"), Close: decFA("105"), Samples: 5},
		},
	}
	conv := &stubConverter{result: apiprices.ConversionResult{Rate: decFA("1.05")}}
	r := newRWAChartEngineFX(t, repo, &stubChartLookup{pair: pairWithRegisteredQuote()}, conv)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/rwa/mars1-usdt/ohlc?interval=1h&in=usd",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	// The wire body has `usd` flattened onto each candle as a nested object.
	var body struct {
		Candles []struct {
			T   string          `json:"t"`
			O   decimal.Decimal `json:"o"`
			C   decimal.Decimal `json:"c"`
			USD struct {
				O      decimal.Decimal `json:"o"`
				H      decimal.Decimal `json:"h"`
				L      decimal.Decimal `json:"l"`
				C      decimal.Decimal `json:"c"`
				Rate   decimal.Decimal `json:"rate"`
				RateTS string          `json:"rate_ts"`
			} `json:"usd"`
		} `json:"candles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body.String())
	}
	if len(body.Candles) != 1 {
		t.Fatalf("candles = %d", len(body.Candles))
	}
	c := body.Candles[0]
	if !c.USD.Rate.Equal(decFA("1.05")) {
		t.Errorf("rate = %s, want 1.05", c.USD.Rate)
	}
	// Native open=100, close=105; converted open=105, close=110.25.
	if !c.USD.O.Equal(decFA("105.00")) {
		t.Errorf("usd.o = %s, want 105", c.USD.O)
	}
	if !c.USD.C.Equal(decFA("110.25")) {
		t.Errorf("usd.c = %s, want 110.25", c.USD.C)
	}
}

func TestRWAOHLC_InMultipleCurrencies_400(t *testing.T) {
	conv := &stubConverter{}
	r := newRWAChartEngineFX(t, &stubCandleRepo{}, &stubChartLookup{pair: pairWithRegisteredQuote()}, conv)
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/ohlc?interval=1h&in=usd,eur", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (cap=1)", w.Code)
	}
}

func TestRWAOHLC_InNoConverter_400(t *testing.T) {
	// nil converter on the chart service — request should 400.
	r := newRWAChartEngineFX(t, &stubCandleRepo{}, &stubChartLookup{pair: pairWithRegisteredQuote()}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/ohlc?interval=1h&in=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no converter)", w.Code)
	}
}

func TestRWASeries_InUSD_FlattensConvertedClose(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &stubCandleRepo{
		candles: []apiprices.Candle{
			{Bucket: bk, Open: decFA("100"), High: decFA("110"), Low: decFA("90"), Close: decFA("105")},
		},
	}
	conv := &stubConverter{result: apiprices.ConversionResult{Rate: decFA("1.50")}}
	r := newRWAChartEngineFX(t, repo, &stubChartLookup{pair: pairWithRegisteredQuote()}, conv)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/series?interval=1h&in=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	// Flat top-level numeric key per point.
	var body struct {
		Points []struct {
			T   string          `json:"t"`
			P   decimal.Decimal `json:"p"`
			USD decimal.Decimal `json:"usd"`
		} `json:"points"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, w.Body.String())
	}
	if !body.Points[0].USD.Equal(decFA("157.50")) {
		t.Errorf("usd = %s, want 157.50 (105 * 1.50)", body.Points[0].USD)
	}
}
