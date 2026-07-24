package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// stubChangeRepo satisfies apiprices.ChangeRepository for handler tests.
type stubChangeRepo struct {
	mu    sync.Mutex // guards mutable fields for concurrent-handler tests (overview)
	res   apiprices.ChangeRepoResult
	err   error
	calls int
	gotQ  apiprices.ChangeQuery
}

func (s *stubChangeRepo) GetChange(_ context.Context, q apiprices.ChangeQuery) (apiprices.ChangeRepoResult, error) {
	s.mu.Lock()
	s.calls++
	s.gotQ = q
	res, err := s.res, s.err
	s.mu.Unlock()
	if err != nil {
		return apiprices.ChangeRepoResult{}, err
	}
	return res, nil
}

func newChangeEngine(_ *testing.T, deps ChangeDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/prices/:token/change", deps.ChangeFT())
	r.GET("/v1/rwa/:symbol/change", deps.ChangeRWA())
	return r
}

// --- FT happy path ---

func TestChangeFT_HappyPath_AllDefaults(t *testing.T) {
	registerTestTokens(t)
	now := time.Now().UTC().Truncate(time.Second)
	repo := &stubChangeRepo{
		res: apiprices.ChangeRepoResult{
			Now: []apiprices.ChangeNow{
				{Currency: "usd", Price: decimal.RequireFromString("0.071541"), TS: now, Found: true},
			},
			Anchors: []apiprices.ChangeAnchor{
				{Currency: "usd", Period: prices.Period24h, Price: decimal.RequireFromString("0.072100"), Bucket: now.Add(-24 * time.Hour), Found: true},
				{Currency: "usd", Period: prices.Period7d, Price: decimal.RequireFromString("0.069300"), Bucket: now.Add(-7 * 24 * time.Hour), Found: true},
				{Currency: "usd", Period: prices.Period30d, Found: false},
			},
		},
	}
	deps := ChangeDeps{
		FTService: &apiprices.ChangeService{
			Repo: repo, Cache: apiprices.NewChangeCache(), Kind: "fa",
		},
		DefaultSource: prices.SourceCoinGecko,
		RWASource:     prices.SourceEquiteez,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/change?currency=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp ftChangeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, w.Body.String())
	}
	if resp.Token != "mvrk" {
		t.Errorf("token = %q, want mvrk", resp.Token)
	}
	if got := resp.Currencies["usd"].Now; got == nil || !got.Equal(decimal.RequireFromString("0.071541")) {
		t.Errorf("now = %v, want 0.071541", got)
	}
	// 24h has data; 30d is null (per missing anchor).
	per24 := resp.Currencies["usd"].Periods["24h"]
	if per24.From == nil || !per24.From.Equal(decimal.RequireFromString("0.0721")) {
		t.Errorf("24h.from = %v, want 0.0721", per24.From)
	}
	per30 := resp.Currencies["usd"].Periods["30d"]
	if per30.From != nil || per30.ChangePct != nil {
		t.Errorf("30d should be all-null (missing history), got %+v", per30)
	}
}

func TestChangeFT_DivByZero_NullChangeButFromKept(t *testing.T) {
	registerTestTokens(t)
	now := time.Now().UTC().Truncate(time.Second)
	repo := &stubChangeRepo{
		res: apiprices.ChangeRepoResult{
			Now: []apiprices.ChangeNow{{Currency: "usd", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
			Anchors: []apiprices.ChangeAnchor{
				{Currency: "usd", Period: prices.Period24h, Price: decimal.NewFromInt(0), Bucket: now.Add(-24 * time.Hour), Found: true},
			},
		},
	}
	deps := ChangeDeps{
		FTService:     &apiprices.ChangeService{Repo: repo, Cache: apiprices.NewChangeCache(), Kind: "fa"},
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/change?currency=usd&periods=24h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp ftChangeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	per := resp.Currencies["usd"].Periods["24h"]
	if per.From == nil || !per.From.IsZero() {
		t.Errorf("from = %v, want 0", per.From)
	}
	if per.FromTS == nil || *per.FromTS == "" {
		t.Error("from_ts must be populated even when p_then=0")
	}
	if per.ChangePct != nil {
		t.Errorf("change_pct must be null on div-by-zero, got %v", per.ChangePct)
	}
	if per.DeltaAbs != nil {
		t.Errorf("delta_abs must be null on div-by-zero, got %v", per.DeltaAbs)
	}
}

func TestChangeFT_UnknownToken_404(t *testing.T) {
	registerTestTokens(t)
	deps := ChangeDeps{
		FTService:     &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "fa"},
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/unknown/change", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestChangeFT_BadCurrency_400(t *testing.T) {
	registerTestTokens(t)
	deps := ChangeDeps{
		FTService:     &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "fa"},
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/change?currency=zzz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "INVALID_ARGUMENT") {
		t.Errorf("body should contain INVALID_ARGUMENT, got %s", w.Body.String())
	}
}

func TestChangeFT_BadPeriod_400(t *testing.T) {
	registerTestTokens(t)
	deps := ChangeDeps{
		FTService:     &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "fa"},
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/change?periods=12h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestChangeFT_AllCurrenciesByDefault(t *testing.T) {
	// When ?currency= is omitted, the handler asks the service for all 10
	// supported currencies (mirrors /latest semantics).
	registerTestTokens(t)
	repo := &stubChangeRepo{}
	deps := ChangeDeps{
		FTService:     &apiprices.ChangeService{Repo: repo, Cache: apiprices.NewChangeCache(), Kind: "fa"},
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/change", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := len(repo.gotQ.Currencies); got != len(prices.AllSupportedCurrencies()) {
		t.Errorf("currencies passed to repo = %d, want %d", got, len(prices.AllSupportedCurrencies()))
	}
}

// --- RWA happy path ---

func TestChangeRWA_HappyPath_NativeQuoteOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pair := prices.RWAPair{ID: 42, BaseSymbol: "mars1", QuoteSymbol: "usdt"}
	repo := &stubChangeRepo{
		res: apiprices.ChangeRepoResult{
			Now: []apiprices.ChangeNow{{Currency: "usdt", Price: decimal.RequireFromString("56.25"), TS: now, Found: true}},
			Anchors: []apiprices.ChangeAnchor{
				{Currency: "usdt", Period: prices.Period24h, Price: decimal.RequireFromString("55.80"), Bucket: now.Add(-24 * time.Hour), Found: true},
			},
		},
	}
	deps := ChangeDeps{
		RWAService: &apiprices.ChangeService{Repo: repo, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		Lookup:     &stubLookup{pair: pair},
		RWASource:  prices.SourceEquiteez,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/change?periods=24h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp rwaChangeResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Symbol != "mars1-usdt" {
		t.Errorf("symbol = %q, want mars1-usdt", resp.Symbol)
	}
	if resp.NativeQuote != "usdt" {
		t.Errorf("native_quote = %q, want usdt", resp.NativeQuote)
	}
	if resp.Now == nil || !resp.Now.Equal(decimal.RequireFromString("56.25")) {
		t.Errorf("now = %v, want 56.25", resp.Now)
	}
	if resp.Periods["24h"].From == nil || !resp.Periods["24h"].From.Equal(decimal.RequireFromString("55.8")) {
		t.Errorf("24h.from = %v, want 55.8", resp.Periods["24h"].From)
	}
}

func TestChangeRWA_InEnabled_ConvertsNow(t *testing.T) {
	// Decision #19 — RWA /change ?in= is wired now that the FX converter
	// honours at-or-before semantics. The handler should request a
	// conversion and surface the converted price as a flat top-level key.
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	now := time.Now().UTC().Truncate(time.Second)
	pair := prices.RWAPair{ID: 42, BaseSymbol: "mars1", QuoteSymbol: "usdt"}
	repo := &stubChangeRepo{
		res: apiprices.ChangeRepoResult{
			Now: []apiprices.ChangeNow{{Currency: "usdt", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
			Anchors: []apiprices.ChangeAnchor{
				{Currency: "usdt", Period: prices.Period24h, Price: decimal.RequireFromString("99"), Bucket: now.Add(-24 * time.Hour), Found: true},
			},
		},
	}
	conv := &stubConverter{result: apiprices.ConversionResult{Rate: decimal.RequireFromString("1.0001")}}
	deps := ChangeDeps{
		RWAService:      &apiprices.ChangeService{Repo: repo, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		Lookup:          &stubLookup{pair: pair},
		Converter:       conv,
		RWASource:       prices.SourceEquiteez,
		MaxInCurrencies: 10,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/change?in=usd&periods=24h", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if conv.calls == 0 {
		t.Errorf("expected at least 1 conversion call for ?in=usd")
	}
	// `usd` should appear as a flat top-level key in the response
	// (matches /v1/rwa/:symbol/latest convention).
	body := w.Body.String()
	if !strings.Contains(body, `"usd":`) {
		t.Errorf("expected `\"usd\":` in flat response, got: %s", body)
	}
}

func TestChangeRWA_InRejected_BadCurrency_400(t *testing.T) {
	pair := prices.RWAPair{ID: 42, BaseSymbol: "mars1", QuoteSymbol: "usdt"}
	deps := ChangeDeps{
		RWAService: &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		Lookup:     &stubLookup{pair: pair},
		Converter:  &stubConverter{},
		RWASource:  prices.SourceEquiteez,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/change?in=zzz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestChangeRWA_InWithoutConverter_400(t *testing.T) {
	// When the server is wired without an FX converter, `?in=` must 400
	// (don't silently swallow the request).
	pair := prices.RWAPair{ID: 42, BaseSymbol: "mars1", QuoteSymbol: "usdt"}
	deps := ChangeDeps{
		RWAService: &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		Lookup:     &stubLookup{pair: pair},
		Converter:  nil,
		RWASource:  prices.SourceEquiteez,
	}
	r := newChangeEngine(t, deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/mars1-usdt/change?in=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestChangeRWA_BadSymbol_400(t *testing.T) {
	deps := ChangeDeps{
		RWAService: &apiprices.ChangeService{Repo: &stubChangeRepo{}, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		Lookup:     &stubLookup{},
		RWASource:  prices.SourceEquiteez,
	}
	r := newChangeEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/broken/change", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- period parsing ---

func TestParsePeriodsParam_Default(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?", nil)
	got, err := parsePeriodsParam(c, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(got) != 3 || got[0] != prices.Period24h || got[1] != prices.Period7d || got[2] != prices.Period30d {
		t.Errorf("default = %v, want [24h 7d 30d]", got)
	}
}

func TestParsePeriodsParam_DedupePreservesOrder(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?periods=24h,7d,24h,1h", nil)
	got, err := parsePeriodsParam(c, 4)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []prices.Period{prices.Period24h, prices.Period7d, prices.Period1h}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("[%d] = %q, want %q", i, got[i], p)
		}
	}
}

func TestParsePeriodsParam_TooMany(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?periods=1h,24h,7d,30d,1h", nil)
	_, err := parsePeriodsParam(c, 4)
	if err == nil {
		t.Fatal("expected error for too many periods")
	}
}

// --- response shapes for unmarshalling in tests ---
//
// num6 marshals as a bare JSON number; unmarshal targets accept decimal.Decimal
// which handles both numeric and string forms.

type ftChangeResponse struct {
	Token      string                              `json:"token"`
	AsOf       string                              `json:"as_of"`
	Currencies map[string]ftChangeCurrencyResponse `json:"currencies"`
}

type ftChangeCurrencyResponse struct {
	Now     *decimal.Decimal                  `json:"now"`
	Periods map[string]ftChangePeriodResponse `json:"periods"`
}

type ftChangePeriodResponse struct {
	FromTS    *string          `json:"from_ts"`
	From      *decimal.Decimal `json:"from"`
	DeltaAbs  *decimal.Decimal `json:"delta_abs"`
	ChangePct *decimal.Decimal `json:"change_pct"`
}

type rwaChangeResponse struct {
	Symbol      string                            `json:"symbol"`
	NativeQuote string                            `json:"native_quote"`
	AsOf        string                            `json:"as_of"`
	Now         *decimal.Decimal                  `json:"now"`
	Periods     map[string]ftChangePeriodResponse `json:"periods"`
}
