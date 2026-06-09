package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// stubLookup satisfies handlers.PairLookup. Returns the configured pair for
// any (base, quote); tracks call count + last (base, quote) so tests can
// assert (a) which symbol was looked up and (b) that the handler does not
// double-resolve.
type stubLookup struct {
	pair     prices.RWAPair
	err      error
	gotBase  string
	gotQuote string
	calls    int
}

func (s *stubLookup) LookupRWAPairBySymbol(_ context.Context, base, quote string) (prices.RWAPair, error) {
	s.gotBase, s.gotQuote = base, quote
	s.calls++
	if s.err != nil {
		return prices.RWAPair{}, s.err
	}
	return s.pair, nil
}

// stubConverter satisfies apiprices.PriceConverter. Returns the configured
// result (applying rate to amount) or err.
type stubConverter struct {
	result apiprices.ConversionResult
	err    error
	calls  int
}

func (s *stubConverter) Convert(
	_ context.Context,
	_ prices.Token,
	_ prices.Currency,
	amount decimal.Decimal,
	_ time.Time,
) (apiprices.ConversionResult, error) {
	s.calls++
	if s.err != nil {
		return apiprices.ConversionResult{}, s.err
	}
	r := s.result
	if r.Rate.IsZero() {
		r.Rate = decimal.NewFromInt(1)
	}
	r.Amount = amount.Mul(r.Rate)
	return r, nil
}

func newRWAEngine(_ *testing.T, deps RWAPriceDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/rwa/:symbol", deps.ListBySymbol())
	r.GET("/v1/rwa/:symbol/latest", deps.LatestBySymbol())
	return r
}

// --- Symbol parsing / bind layer ---

func TestParseRWASymbol(t *testing.T) {
	cases := []struct {
		in          string
		base, quote string
		ok          bool
	}{
		{"mars1-usdt", "mars1", "usdt", true},
		{"MARS1-USDT", "mars1", "usdt", true},   // uppercased lowered
		{" Mars1-USDT ", "mars1", "usdt", true}, // trimmed
		{"x-at-usdt", "x-at", "usdt", true},     // split on LAST hyphen
		{"a-b", "a", "b", true},
		{"", "", "", false},                                   // empty
		{"mars1", "", "", false},                              // no hyphen
		{"-usdt", "", "", false},                              // empty base
		{"mars1-", "", "", false},                             // empty quote
		{string(make([]byte, maxSymbolLen+1)), "", "", false}, // too long
	}
	for _, c := range cases {
		base, quote, ok := parseRWASymbol(c.in)
		if ok != c.ok || base != c.base || quote != c.quote {
			t.Errorf("parseRWASymbol(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, base, quote, ok, c.base, c.quote, c.ok)
		}
	}
}

func TestRWA_BadSymbolFormat_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		Lookup:        &stubLookup{},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	for _, path := range []string{"/v1/rwa/mars1", "/v1/rwa/-usdt", "/v1/rwa/mars1-"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, w.Code)
		}
	}
}

// Numeric path (`/v1/rwa/42`) — historical pair_id form — must be rejected
// as a malformed symbol now that the route is symbol-only.
func TestRWA_NumericPath_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		Lookup:        &stubLookup{},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/42", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for numeric path (no hyphen)", w.Code)
	}
}

func TestRWA_LatestNoData_404(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{}, // empty points
		Lookup:        &stubLookup{pair: prices.RWAPair{ID: 42, QuoteSymbol: "USDT"}},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/latest", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// Pair lookup returns ErrPairNotFound → 404.
func TestRWA_SymbolNotFound_404(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		Lookup:        &stubLookup{err: prices.ErrPairNotFound},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/unknown-usdt/latest", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for unknown symbol", w.Code)
	}
}

// Pair lookup returns *PairAmbiguousError → 409 with `details.pair_ids` populated.
func TestRWA_SymbolAmbiguous_409(t *testing.T) {
	lookup := &stubLookup{err: &prices.PairAmbiguousError{
		Base: "mars1", Quote: "usdt", IDs: []int64{42, 77},
	}}
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for ambiguous symbol; body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details struct {
			PairIDs []int64 `json:"pair_ids"`
		} `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if got.Code != "CONFLICT" {
		t.Errorf("code = %q, want CONFLICT", got.Code)
	}
	if len(got.Details.PairIDs) != 2 || got.Details.PairIDs[0] != 42 || got.Details.PairIDs[1] != 77 {
		t.Errorf("details.pair_ids = %v, want [42,77]", got.Details.PairIDs)
	}
}

// --- ?in= multi-currency tests ---

// rwaLastPointForTests returns a stub query service with one `last` point
// and a USDT-registered lookup.
func rwaLastPointForTests(t *testing.T) (*stubQueryService, *stubLookup) {
	t.Helper()
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "last", Price: decimal.NewFromFloat(100.00)},
		},
	}
	lookup := &stubLookup{
		pair: prices.RWAPair{
			ID:          42,
			Source:      prices.SourceEquiteez,
			BaseSymbol:  "TST",
			QuoteSymbol: "USDT",
			Enabled:     true,
		},
	}
	return svc, lookup
}

func TestRWA_Latest_InNotEnabled_400(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	deps := RWAPriceDeps{
		Service:       svc,
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
		// No Converter → ?in= must be rejected.
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when ?in= used without converter wired", w.Code)
	}
}

func TestRWA_Latest_BadInCurrency_400(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       &stubConverter{},
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=xyz", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown ?in= currency", w.Code)
	}
}

func TestRWA_Latest_TooManyInCurrencies_400(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       &stubConverter{},
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 2,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd,eur,gbp", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when ?in= exceeds MaxInCurrencies", w.Code)
	}
}

// TestRWA_Latest_InUSD_Success — happy path: USDT pair, rate 1.0001 → USD.
// Verifies flat response shape: native_quote, price as number, usd as number.
// Also verifies that the symbol resolver fires exactly once per request.
func TestRWA_Latest_InUSD_Success(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	rate := decimal.NewFromFloat(1.0001)
	conv := &stubConverter{result: apiprices.ConversionResult{
		Rate:   rate,
		Source: prices.SourceCoinGecko,
		RateTS: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
	}}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if lookup.calls != 1 {
		t.Errorf("lookup.calls = %d, want 1 (single resolve per request)", lookup.calls)
	}
	if lookup.gotBase != "tst" || lookup.gotQuote != "usdt" {
		t.Errorf("lookup got (%q,%q), want (tst,usdt)", lookup.gotBase, lookup.gotQuote)
	}

	// Decode into a flat map so we can check both fixed and dynamic keys.
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if got["native_quote"] != "usdt" {
		t.Errorf("native_quote = %v, want usdt", got["native_quote"])
	}
	// price should be JSON number (float64 after unmarshal), not a string.
	price, ok := got["price"].(float64)
	if !ok {
		t.Fatalf("price is %T (%v), want float64 (JSON number)", got["price"], got["price"])
	}
	if price != 100.0 {
		t.Errorf("price = %v, want 100.0", price)
	}
	// 100.00 * 1.0001 = 100.01 → rounded to 6 dp → 100.01 (exact).
	usd, ok := got["usd"].(float64)
	if !ok {
		t.Fatalf("usd is %T (%v), want float64; body=%s", got["usd"], got["usd"], w.Body.String())
	}
	if usd != 100.01 {
		t.Errorf("usd = %v, want 100.01", usd)
	}
	if conv.calls != 1 {
		t.Errorf("converter called %d times, want 1 (one last point)", conv.calls)
	}
}

// TestRWA_Latest_NumericJSON — price is a JSON number, not a quoted string.
func TestRWA_Latest_NumericJSON(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	deps := RWAPriceDeps{
		Service:       svc,
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["price"].(float64); !ok {
		t.Errorf("price type = %T, want float64 (JSON number); body=%s", got["price"], w.Body.String())
	}
}

// TestRWA_Latest_RoundedTo6 — converter returns high-precision amount; response
// rounds to 6 decimal places.
func TestRWA_Latest_RoundedTo6(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	// rate produces 33.99992345678... after multiplication with 100
	conv := &stubConverter{result: apiprices.ConversionResult{
		Rate:   decimal.RequireFromString("0.3399992345678"),
		Source: prices.SourceCoinGecko,
		RateTS: time.Now().UTC(),
	}}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	usd, ok := got["usd"].(float64)
	if !ok {
		t.Fatalf("usd type = %T, want float64", got["usd"])
	}
	// 100 * 0.3399992345678 = 33.99992345678 → Round(6) = 33.999923
	if usd != 33.999923 {
		t.Errorf("usd = %v, want 33.999923 (rounded to 6 dp)", usd)
	}
}

// TestRWA_Latest_InEUR_NoFXRate — converter returns ErrNoFXRate; response is
// 200 with `eur` key absent from the flat object.
func TestRWA_Latest_InEUR_NoFXRate(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	conv := &stubConverter{err: apiprices.ErrNoFXRate}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=eur", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (partial success); body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := got["eur"]; present {
		t.Errorf("eur key should be absent when conversion fails, got %v", got["eur"])
	}
	// native price must still be present.
	if _, ok := got["price"].(float64); !ok {
		t.Errorf("price missing or wrong type; body=%s", w.Body.String())
	}
}

// TestRWA_Latest_InMulti_PartialSuccess — `?in=usd,eur` where USD succeeds
// and EUR fails. `usd` key present, `eur` key absent.
func TestRWA_Latest_InMulti_PartialSuccess(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	conv := &stubFnConverter{
		fn: func(_ context.Context, _ prices.Token, target prices.Currency, amount decimal.Decimal, _ time.Time) (apiprices.ConversionResult, error) {
			if target == prices.CurrencyEUR {
				return apiprices.ConversionResult{}, apiprices.ErrNoFXRate
			}
			return apiprices.ConversionResult{
				Amount: amount,
				Rate:   decimal.NewFromInt(1),
				Source: prices.SourceCoinGecko,
				RateTS: time.Now().UTC(),
			}, nil
		},
	}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd,eur", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["usd"].(float64); !ok {
		t.Errorf("usd key missing or wrong type; body=%s", w.Body.String())
	}
	if _, present := got["eur"]; present {
		t.Errorf("eur key should be absent when conversion fails")
	}
}

// TestRWA_Latest_QuoteNotInRegistry — pair quoted in a token not in the FT
// `tokens` registry. Converter is never called; all ?in= keys absent.
// Native `price` still returned.
func TestRWA_Latest_QuoteNotInRegistry(t *testing.T) {
	prices.RegisterTokens([]prices.TokenInfo{
		// usdt intentionally NOT registered to simulate the case.
		{Symbol: "mvrk", Name: "Mavryk", Decimals: 6, Enabled: true},
	})
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "last", Price: decimal.NewFromFloat(100.00)},
		},
	}
	lookup := &stubLookup{pair: prices.RWAPair{
		ID: 42, Source: prices.SourceEquiteez, QuoteSymbol: "USDT", Enabled: true,
	}}
	conv := &stubConverter{}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if conv.calls != 0 {
		t.Errorf("converter called %d times, want 0 (quote should fail registry check first)", conv.calls)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, present := got["usd"]; present {
		t.Errorf("usd key should be absent when quote not in registry; body=%s", w.Body.String())
	}
	if _, ok := got["price"].(float64); !ok {
		t.Errorf("price missing or wrong type; body=%s", w.Body.String())
	}
}

// TestRWA_List_SameShapeAsLatest — list endpoint returns an array where each
// element has the same shape as the /latest response.
func TestRWA_List_SameShapeAsLatest(t *testing.T) {
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now.Add(-time.Hour), Metric: "last", Price: decimal.NewFromFloat(99.0)},
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "last", Price: decimal.NewFromFloat(100.0)},
		},
	}
	lookup := &stubLookup{pair: prices.RWAPair{
		ID: 42, Source: prices.SourceEquiteez, QuoteSymbol: "USDT", Enabled: true,
	}}
	conv := &stubConverter{result: apiprices.ConversionResult{
		Rate:   decimal.NewFromFloat(1.0001),
		Source: prices.SourceCoinGecko,
		RateTS: now,
	}}
	deps := RWAPriceDeps{
		Service:         svc,
		Converter:       conv,
		Lookup:          lookup,
		DefaultSource:   prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt?in=usd", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if _, ok := row["timestamp"].(string); !ok {
			t.Errorf("row[%d].timestamp missing or not string", i)
		}
		if row["native_quote"] != "usdt" {
			t.Errorf("row[%d].native_quote = %v, want usdt", i, row["native_quote"])
		}
		if _, ok := row["price"].(float64); !ok {
			t.Errorf("row[%d].price is %T, want float64 (JSON number)", i, row["price"])
		}
		if _, ok := row["usd"].(float64); !ok {
			t.Errorf("row[%d].usd is %T, want float64", i, row["usd"])
		}
		// No old-format keys present.
		for _, badKey := range []string{"side", "values", "in", "source", "entity", "size"} {
			if _, present := row[badKey]; present {
				t.Errorf("row[%d] has unexpected key %q", i, badKey)
			}
		}
	}
}

// TestRWA_List_OnlyLastSide — query service must be called with Metrics == ["last"].
func TestRWA_List_OnlyLastSide(t *testing.T) {
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	svc := &stubQueryService{}
	lookup := &stubLookup{pair: prices.RWAPair{
		ID: 42, Source: prices.SourceEquiteez, QuoteSymbol: "USDT", Enabled: true,
	}}
	deps := RWAPriceDeps{
		Service:       svc,
		Lookup:        lookup,
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      1000,
		DefaultLimit:  100,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if len(svc.gotQ.Metrics) != 1 || svc.gotQ.Metrics[0] != "last" {
		t.Errorf("query Metrics = %v, want [last]", svc.gotQ.Metrics)
	}
}

// TestRWA_List_EmptyResult_200 — empty list returns [] (200), not 404.
func TestRWA_List_EmptyResult_200(t *testing.T) {
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	deps := RWAPriceDeps{
		Service:       &stubQueryService{}, // no points
		Lookup:        &stubLookup{pair: prices.RWAPair{ID: 42, QuoteSymbol: "USDT"}},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if body != "[]" && body != "[]\n" {
		// tolerate trailing newline from JSON encoder
		t.Errorf("body = %q, want []", body)
	}
}

// stubFnConverter routes Convert to a user-supplied function. Useful for
// per-target behavior in partial-success tests.
type stubFnConverter struct {
	fn func(ctx context.Context, src prices.Token, target prices.Currency, amt decimal.Decimal, ts time.Time) (apiprices.ConversionResult, error)
}

func (s *stubFnConverter) Convert(
	ctx context.Context, src prices.Token, target prices.Currency, amt decimal.Decimal, ts time.Time,
) (apiprices.ConversionResult, error) {
	return s.fn(ctx, src, target, amt, ts)
}

// stubStats satisfies RWAStatsReader. Returns the configured ath / price-at
// values and records call counts so tests can assert the reader is consulted.
type stubStats struct {
	athPrice decimal.Decimal
	athTS    time.Time
	athFound bool
	athErr   error
	p1yPrice decimal.Decimal
	p1yTS    time.Time
	p1yFound bool
	p1yErr   error
	athCalls int
	p1yCalls int
}

func (s *stubStats) AllTimeHighLast(_ context.Context, _ int64, _ string) (decimal.Decimal, time.Time, bool, error) {
	s.athCalls++
	return s.athPrice, s.athTS, s.athFound, s.athErr
}

func (s *stubStats) PriceAtOrBefore(_ context.Context, _ int64, _ string, _ time.Time) (decimal.Decimal, time.Time, bool, error) {
	s.p1yCalls++
	return s.p1yPrice, s.p1yTS, s.p1yFound, s.p1yErr
}

// TestRWA_Latest_StatsEnriched — with a Stats reader, /latest carries the
// `ath {price,date}` and `price_one_year_ago {price}` blocks (native quote, no `?in=`).
func TestRWA_Latest_StatsEnriched(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	stats := &stubStats{
		athPrice: decimal.NewFromFloat(161.0),
		athTS:    time.Date(2025, 10, 6, 0, 0, 0, 0, time.UTC),
		athFound: true,
		p1yPrice: decimal.NewFromFloat(84.0),
		p1yTS:    time.Date(2025, 4, 28, 12, 0, 0, 0, time.UTC),
		p1yFound: true,
	}
	deps := RWAPriceDeps{Service: svc, Lookup: lookup, DefaultSource: prices.SourceEquiteez, Stats: stats}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}

	ath, ok := got["ath"].(map[string]any)
	if !ok {
		t.Fatalf("ath missing or not an object: %v; body=%s", got["ath"], w.Body.String())
	}
	if p, ok := ath["price"].(float64); !ok || p != 161.0 {
		t.Errorf("ath.price = %v, want 161.0", ath["price"])
	}
	if ath["date"] != "2025-10-06" {
		t.Errorf("ath.date = %v, want 2025-10-06", ath["date"])
	}

	p1y, ok := got["price_one_year_ago"].(map[string]any)
	if !ok {
		t.Fatalf("price_one_year_ago missing or not an object: %v", got["price_one_year_ago"])
	}
	if p, ok := p1y["price"].(float64); !ok || p != 84.0 {
		t.Errorf("price_one_year_ago.price = %v, want 84.0", p1y["price"])
	}
}

// TestRWA_Latest_StatsConverted — `?in=usd` converts the ath and year-ago
// blocks at their own timestamps, inlined as flat currency keys inside each.
func TestRWA_Latest_StatsConverted(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	conv := &stubConverter{result: apiprices.ConversionResult{
		Rate:   decimal.NewFromInt(2),
		Source: prices.SourceCoinGecko,
		RateTS: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
	}}
	stats := &stubStats{
		athPrice: decimal.NewFromFloat(161.0),
		athTS:    time.Date(2025, 10, 6, 0, 0, 0, 0, time.UTC),
		athFound: true,
		p1yPrice: decimal.NewFromFloat(84.0),
		p1yTS:    time.Date(2025, 4, 28, 12, 0, 0, 0, time.UTC),
		p1yFound: true,
	}
	deps := RWAPriceDeps{
		Service: svc, Converter: conv, Lookup: lookup,
		DefaultSource: prices.SourceEquiteez, MaxInCurrencies: 10, Stats: stats,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest?in=usd", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}

	ath := got["ath"].(map[string]any)
	if usd, ok := ath["usd"].(float64); !ok || usd != 322.0 { // 161 * 2
		t.Errorf("ath.usd = %v, want 322.0", ath["usd"])
	}
	p1y := got["price_one_year_ago"].(map[string]any)
	if usd, ok := p1y["usd"].(float64); !ok || usd != 168.0 { // 84 * 2
		t.Errorf("price_one_year_ago.usd = %v, want 168.0", p1y["usd"])
	}
}

// TestRWA_Latest_NoStats_OmitsBlocks — with no Stats reader wired, the ath /
// price_one_year_ago keys are absent (back-compat with existing consumers).
func TestRWA_Latest_NoStats_OmitsBlocks(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	deps := RWAPriceDeps{Service: svc, Lookup: lookup, DefaultSource: prices.SourceEquiteez}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["ath"]; ok {
		t.Errorf("ath present without a Stats reader")
	}
	if _, ok := got["price_one_year_ago"]; ok {
		t.Errorf("price_one_year_ago present without a Stats reader")
	}
}

// TestRWA_Latest_StatsNotFound_OmitsBlocks — the reader is consulted but has no
// data (found=false); the blocks are omitted and the core price still returns 200.
func TestRWA_Latest_StatsNotFound_OmitsBlocks(t *testing.T) {
	svc, lookup := rwaLastPointForTests(t)
	stats := &stubStats{athFound: false, p1yFound: false}
	deps := RWAPriceDeps{Service: svc, Lookup: lookup, DefaultSource: prices.SourceEquiteez, Stats: stats}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/tst-usdt/latest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got["ath"]; ok {
		t.Errorf("ath present when reader returned found=false")
	}
	if _, ok := got["price_one_year_ago"]; ok {
		t.Errorf("price_one_year_ago present when reader returned found=false")
	}
	if stats.athCalls != 1 || stats.p1yCalls != 1 {
		t.Errorf("reader calls = (ath %d, p1y %d), want (1,1)", stats.athCalls, stats.p1yCalls)
	}
}
