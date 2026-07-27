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

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newOverviewEngine(pairs []prices.RWAPair, changeRes apiprices.ChangeRepoResult, candles []apiprices.Candle) *gin.Engine {
	gin.SetMode(gin.TestMode)
	deps := RWAOverviewDeps{
		Pairs: &stubPairsLister{pairs: pairs},
		Change: &apiprices.ChangeService{
			Repo:  &stubChangeRepo{res: changeRes},
			Cache: apiprices.NewChangeCache(),
			Kind:  "rwa",
		},
		Charts: &apiprices.ChartService{
			Repo:     &stubCandleRepo{candles: candles},
			Caps:     apiprices.DefaultCaps(),
			MaxLimit: 1000,
			Kind:     "rwa",
		},
		Source: prices.SourceEquiteez,
	}
	r := gin.New()
	r.GET("/v1/rwa", deps.List())
	return r
}

func getJSON(t *testing.T, r *gin.Engine, url string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	return out
}

func TestRWAOverview_ListHappyPath(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	pairs := []prices.RWAPair{
		{ID: 2, BaseSymbol: "mars2", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez, TokenAddr: "KT1MarsToken"},
	}
	changeRes := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("100.42"), TS: now, Found: true}},
		Anchors: []apiprices.ChangeAnchor{
			{Currency: "usdt", Period: prices.Period24h, Price: dec("99.22"), Bucket: now.Add(-24 * time.Hour), Found: true},
		},
	}
	candles := []apiprices.Candle{
		{Bucket: now.Add(-30 * time.Minute), Open: dec("100.10"), High: dec("100.50"), Low: dec("100.00"), Close: dec("100.10"), Samples: 3},
		{Bucket: now.Add(-15 * time.Minute), Open: dec("100.10"), High: dec("100.60"), Low: dec("100.05"), Close: dec("100.42"), Samples: 4},
	}
	r := newOverviewEngine(pairs, changeRes, candles)

	body := getJSON(t, r, "/v1/rwa")

	assets, ok := body["assets"].([]any)
	if !ok || len(assets) != 2 {
		t.Fatalf("assets = %v, want 2 entries", body["assets"])
	}
	// Stable order: mars1 before mars2.
	first := assets[0].(map[string]any)
	if first["symbol"] != "mars1-usdt" {
		t.Errorf("first symbol = %v, want mars1-usdt (sorted)", first["symbol"])
	}
	if first["native_quote"] != "usdt" {
		t.Errorf("native_quote = %v, want usdt", first["native_quote"])
	}
	if first["token_address"] != "KT1MarsToken" {
		t.Errorf("token_address = %v, want KT1MarsToken", first["token_address"])
	}
	if second := assets[1].(map[string]any); second["token_address"] != nil {
		t.Errorf("mars2 token_address = %v, want null", second["token_address"])
	}
	if first["price"] != 100.42 {
		t.Errorf("price = %v, want 100.42", first["price"])
	}
	ch := first["change_24h"].(map[string]any)
	if ch["delta_abs"] == nil || ch["change_pct"] == nil {
		t.Errorf("change_24h must be populated, got %v", ch)
	}
	// delta_abs = 100.42 - 99.22 = 1.20
	if da, _ := ch["delta_abs"].(float64); da < 1.19 || da > 1.21 {
		t.Errorf("delta_abs = %v, want ~1.20", ch["delta_abs"])
	}
	series := first["series_1d"].(map[string]any)
	if series["interval"] != "15m" {
		t.Errorf("series interval = %v, want 15m", series["interval"])
	}
	pts := series["points"].([]any)
	if len(pts) != 2 {
		t.Fatalf("series points = %d, want 2", len(pts))
	}
	p0 := pts[0].(map[string]any)
	if _, ok := p0["t"]; !ok {
		t.Error("series point missing 't'")
	}
	if p0["p"] != 100.10 {
		t.Errorf("first point p = %v, want 100.10", p0["p"])
	}
}

func TestRWAOverview_EmptyCatalog(t *testing.T) {
	r := newOverviewEngine(nil, apiprices.ChangeRepoResult{}, nil)
	body := getJSON(t, r, "/v1/rwa")
	assets, ok := body["assets"].([]any)
	if !ok || len(assets) != 0 {
		t.Errorf("assets should be [], got %v", body["assets"])
	}
}

func TestRWAOverview_ResponseCache(t *testing.T) {
	now := time.Now().UTC()
	lister := &stubPairsLister{pairs: []prices.RWAPair{
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
	}}
	changeRes := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("1"), TS: now, Found: true}},
	}
	deps := NewRWAOverviewDeps(
		lister,
		nil,
		&apiprices.ChangeService{Repo: &stubChangeRepo{res: changeRes}, Cache: apiprices.NewChangeCache(), Kind: "rwa"},
		&apiprices.ChartService{Repo: &stubCandleRepo{}, Caps: apiprices.DefaultCaps(), MaxLimit: 1000, Kind: "rwa"},
		nil, prices.SourceEquiteez, time.Minute,
	)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/rwa", deps.List())

	getJSON(t, r, "/v1/rwa")
	getJSON(t, r, "/v1/rwa")
	if lister.calls != 1 {
		t.Errorf("pairs lister called %d times, want 1 (second request served from cache)", lister.calls)
	}
	// A different query shape (limit) must not hit the same cache entry.
	getJSON(t, r, "/v1/rwa?limit=1")
	if lister.calls != 2 {
		t.Errorf("distinct limit should rebuild; lister calls = %d, want 2", lister.calls)
	}
}

func TestRWAOverview_LimitClamps(t *testing.T) {
	now := time.Now().UTC()
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
		{ID: 2, BaseSymbol: "mars2", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
		{ID: 3, BaseSymbol: "mars3", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
	}
	changeRes := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("1"), TS: now, Found: true}},
	}
	r := newOverviewEngine(pairs, changeRes, nil)

	body := getJSON(t, r, "/v1/rwa?limit=2")
	if assets := body["assets"].([]any); len(assets) != 2 {
		t.Errorf("limit=2 must cap to 2 assets, got %d", len(assets))
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa?limit=0", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("limit=0 should be 400, got %d", w.Code)
	}
}

// stubLaunchLister satisfies RWALaunchLister for handler tests.
type stubLaunchLister struct {
	launches []prices.RWALaunch
	err      error
}

func (s *stubLaunchLister) EnabledLaunches(_ context.Context, _ prices.Source) ([]prices.RWALaunch, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.launches, nil
}

func khbeLaunch() prices.RWALaunch {
	return prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1KHBE", BaseSymbol: "khbe", QuoteSymbol: "usdt",
		LaunchID: 6, Name: "KHBE-issuance-v2", Status: "active", Active: true,
		Price:           dec("100"),
		TotalBought:     dec("6667"),
		MaxAmountCap:    dec("2500000000000"),
		ProgressPercent: 2.667e-7,
		Enabled:         true,
	}
}

func overviewEngineWith(pairs []prices.RWAPair, res apiprices.ChangeRepoResult, launches RWALaunchLister) *gin.Engine {
	gin.SetMode(gin.TestMode)
	deps := RWAOverviewDeps{
		Pairs:    &stubPairsLister{pairs: pairs},
		Launches: launches,
		Change: &apiprices.ChangeService{
			Repo: &stubChangeRepo{res: res}, Cache: apiprices.NewChangeCache(), Kind: "rwa",
		},
		Charts: &apiprices.ChartService{
			Repo: &stubCandleRepo{}, Caps: apiprices.DefaultCaps(), MaxLimit: 1000, Kind: "rwa",
		},
		Source: prices.SourceEquiteez,
	}
	r := gin.New()
	r.GET("/v1/rwa", deps.List())
	return r
}

// TestRWAOverview_PrimaryIssuanceAssets: a token sold on the launchpad has no
// orderbook (so no rwa_pairs row) and must still appear in the catalog, priced
// at its base tier with sale progress instead of a market quote.
func TestRWAOverview_PrimaryIssuanceAssets(t *testing.T) {
	r := overviewEngineWith(nil, apiprices.ChangeRepoResult{}, &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}})
	body := getJSON(t, r, "/v1/rwa")

	assets := body["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(assets))
	}
	a := assets[0].(map[string]any)
	if a["symbol"] != "khbe-usdt" {
		t.Errorf("symbol = %v, want khbe-usdt", a["symbol"])
	}
	if a["market"] != "primary_issuance" {
		t.Errorf("market = %v, want primary_issuance", a["market"])
	}
	if a["price"] != 100.0 {
		t.Errorf("price = %v, want 100 (base tier)", a["price"])
	}
	pi, ok := a["primary_issuance"].(map[string]any)
	if !ok {
		t.Fatalf("primary_issuance block missing: %v", a)
	}
	if pi["total_bought"] != "6667" {
		t.Errorf("total_bought = %v, want \"6667\" (raw string)", pi["total_bought"])
	}
	if pi["max_amount_cap"] != "2500000000000" {
		t.Errorf("max_amount_cap = %v, want \"2500000000000\"", pi["max_amount_cap"])
	}
	if got, _ := pi["progress_percent"].(float64); got < 2.6e-7 || got > 2.7e-7 {
		t.Errorf("progress_percent = %v, want ~2.667e-7 (must not collapse to 0)", pi["progress_percent"])
	}
	if pi["status"] != "active" || pi["active"] != true {
		t.Errorf("status/active = %v/%v, want active/true", pi["status"], pi["active"])
	}
	// No trades exist yet: empty series, null change.
	if pts := a["series_1d"].(map[string]any)["points"].([]any); len(pts) != 0 {
		t.Errorf("series points = %d, want 0", len(pts))
	}
	if ch := a["change_24h"].(map[string]any); ch["delta_abs"] != nil {
		t.Errorf("change_24h.delta_abs = %v, want null", ch["delta_abs"])
	}
}

// TestRWAOverview_LaunchNotDuplicatedWhenOrderbookExists: a token that also
// trades on an orderbook must appear once — the live market row wins.
func TestRWAOverview_LaunchNotDuplicatedWhenOrderbookExists(t *testing.T) {
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "khbe", QuoteSymbol: "USDT", Source: prices.SourceEquiteez, TokenAddr: "KT1KHBE"},
	}
	res := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("42"), TS: time.Now().UTC(), Found: true}},
	}
	r := overviewEngineWith(pairs, res, &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}})
	body := getJSON(t, r, "/v1/rwa")

	assets := body["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("assets = %d, want 1 (orderbook row wins, launch skipped)", len(assets))
	}
	a := assets[0].(map[string]any)
	if a["market"] != "orderbook" {
		t.Errorf("market = %v, want orderbook", a["market"])
	}
	if a["price"] != 42.0 {
		t.Errorf("price = %v, want the live orderbook price 42", a["price"])
	}
}

// TestRWAOverview_LaunchListerErrorIsNotFatal: the orderbook side must still be
// served when the launch catalog is unavailable.
func TestRWAOverview_LaunchListerErrorIsNotFatal(t *testing.T) {
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
	}
	res := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("50"), TS: time.Now().UTC(), Found: true}},
	}
	r := overviewEngineWith(pairs, res, &stubLaunchLister{err: context.DeadlineExceeded})
	body := getJSON(t, r, "/v1/rwa")
	if len(body["assets"].([]any)) != 1 {
		t.Errorf("orderbook assets must survive a launch-lister failure, got %v", body["assets"])
	}
}
