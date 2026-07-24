package handlers

import (
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
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
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

	if body["as_of"] == nil {
		t.Error("as_of should be set when at least one asset has a latest price")
	}
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
	if body["as_of"] != nil {
		t.Errorf("as_of should be null for empty catalog, got %v", body["as_of"])
	}
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
