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

// stubConverter satisfies apiprices.PriceConverter. Returns one of:
//   - configured `result` (with Identity flag honored)
//   - `err` (returned as-is, caller maps to fx.error)
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
		in            string
		base, quote   string
		ok            bool
	}{
		{"mars1-usdt", "mars1", "usdt", true},
		{"MARS1-USDT", "mars1", "usdt", true},   // uppercased lowered
		{" Mars1-USDT ", "mars1", "usdt", true}, // trimmed
		{"x-at-usdt", "x-at", "usdt", true},     // split on LAST hyphen
		{"a-b", "a", "b", true},
		{"", "", "", false},                                       // empty
		{"mars1", "", "", false},                                  // no hyphen
		{"-usdt", "", "", false},                                  // empty base
		{"mars1-", "", "", false},                                 // empty quote
		{string(make([]byte, maxSymbolLen+1)), "", "", false},     // too long
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

func TestRWA_BadSide_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		Lookup:        &stubLookup{pair: prices.RWAPair{ID: 42, QuoteSymbol: "USDT"}},
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      1000,
	}
	r := newRWAEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt?side=foo", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown side", w.Code)
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

// rwaSnapshotForTests returns a stub query service that emits one bid+ask
// snapshot, plus a registered USDT token registry for the converter.
func rwaSnapshotForTests(t *testing.T) (*stubQueryService, *stubLookup) {
	t.Helper()
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "bid", Price: decimal.NewFromFloat(100.00)},
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "ask", Price: decimal.NewFromFloat(101.00)},
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
	svc, lookup := rwaSnapshotForTests(t)
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
	svc, lookup := rwaSnapshotForTests(t)
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
	svc, lookup := rwaSnapshotForTests(t)
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

// TestRWA_Latest_InUSD_Success — happy path: USDT pair → USD with rate 1.0001.
// Verifies the response shape: native_quote, values, in.usd.values, in.usd.fx.
// Also verifies that the symbol resolver fires exactly once per request (no
// double-lookup that the legacy ?in= flow had).
func TestRWA_Latest_InUSD_Success(t *testing.T) {
	svc, lookup := rwaSnapshotForTests(t)
	rate := decimal.NewFromFloat(1.0001)
	rateTS := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	conv := &stubConverter{result: apiprices.ConversionResult{
		Rate:   rate,
		Source: prices.SourceCoinGecko,
		RateTS: rateTS,
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
	var got struct {
		NativeQuote string            `json:"native_quote"`
		Values      map[string]string `json:"values"`
		In          map[string]struct {
			Values map[string]string `json:"values"`
			FX     struct {
				Rate   string `json:"rate"`
				Source string `json:"source"`
				Method string `json:"method"`
			} `json:"fx"`
		} `json:"in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if got.NativeQuote != "usdt" {
		t.Errorf("native_quote = %q, want usdt", got.NativeQuote)
	}
	usd, ok := got.In["usd"]
	if !ok {
		t.Fatalf("in.usd missing in response: %s", w.Body.String())
	}
	if got, want := usd.FX.Source, "coingecko"; got != want {
		t.Errorf("fx.source = %q, want %q", got, want)
	}
	if usd.FX.Method != "rate" {
		t.Errorf("fx.method = %q, want rate", usd.FX.Method)
	}
	// 100.00 USDT * 1.0001 = 100.01 USD.
	if got, want := usd.Values["bid"], "100.01"; got != want {
		t.Errorf("in.usd.bid = %q, want %q", got, want)
	}
	if got, want := usd.Values["ask"], "101.0101"; got != want {
		t.Errorf("in.usd.ask = %q, want %q", got, want)
	}
	if conv.calls != 2 {
		t.Errorf("converter called %d times, want 2 (bid+ask)", conv.calls)
	}
}

// TestRWA_Latest_InEUR_NoFXRate — converter returns ErrNoFXRate; response is
// 200 with `in.eur.fx.error: "no_fx_rate"` and no values block.
func TestRWA_Latest_InEUR_NoFXRate(t *testing.T) {
	svc, lookup := rwaSnapshotForTests(t)
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
	var got struct {
		In map[string]struct {
			Values map[string]string `json:"values,omitempty"`
			FX     struct {
				Error string `json:"error"`
			} `json:"fx"`
		} `json:"in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eur, ok := got.In["eur"]
	if !ok {
		t.Fatalf("in.eur missing: %s", w.Body.String())
	}
	if eur.FX.Error != "no_fx_rate" {
		t.Errorf("fx.error = %q, want no_fx_rate", eur.FX.Error)
	}
	if len(eur.Values) != 0 {
		t.Errorf("values should be empty when conversion fails, got %v", eur.Values)
	}
}

// TestRWA_Latest_InMulti_PartialSuccess — `?in=usd,eur` where USD succeeds
// and EUR fails. USD block has values + fx.rate; EUR block has fx.error only.
func TestRWA_Latest_InMulti_PartialSuccess(t *testing.T) {
	svc, lookup := rwaSnapshotForTests(t)
	calls := 0
	conv := &stubFnConverter{
		fn: func(_ context.Context, _ prices.Token, target prices.Currency, amount decimal.Decimal, _ time.Time) (apiprices.ConversionResult, error) {
			calls++
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
	var got struct {
		In map[string]struct {
			Values map[string]string `json:"values,omitempty"`
			FX     struct {
				Error string `json:"error"`
				Rate  string `json:"rate"`
			} `json:"fx"`
		} `json:"in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := got.In["usd"]; !ok {
		t.Errorf("in.usd missing")
	}
	if got.In["usd"].FX.Error != "" {
		t.Errorf("usd.fx.error = %q, want empty", got.In["usd"].FX.Error)
	}
	if got.In["eur"].FX.Error != "no_fx_rate" {
		t.Errorf("eur.fx.error = %q, want no_fx_rate", got.In["eur"].FX.Error)
	}
}

// TestRWA_Latest_QuoteNotInRegistry — pair quoted in a token not in the FT
// `tokens` registry. Converter is never called; fx.error is set per target.
func TestRWA_Latest_QuoteNotInRegistry(t *testing.T) {
	prices.RegisterTokens([]prices.TokenInfo{
		// usdt intentionally NOT registered to simulate the case.
		{Symbol: "mvrk", Name: "Mavryk", Decimals: 6, Enabled: true},
	})
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	svc := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceEquiteez, EntityKey: "42", Timestamp: now, Metric: "bid", Price: decimal.NewFromFloat(100.00)},
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
	var got struct {
		In map[string]struct {
			FX struct {
				Error string `json:"error"`
			} `json:"fx"`
		} `json:"in"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.In["usd"].FX.Error != "quote_currency_not_in_registry" {
		t.Errorf("fx.error = %q, want quote_currency_not_in_registry", got.In["usd"].FX.Error)
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

// silence vet about unused imports if test file shrinks
var _ = errors.New
