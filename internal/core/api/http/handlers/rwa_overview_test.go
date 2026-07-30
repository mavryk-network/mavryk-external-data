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
	if a["market"] != "primary" {
		t.Errorf("market = %v, want primary", a["market"])
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

// TestRWAOverview_OverlapKeepsBothFacets: an asset that trades on an orderbook
// AND is still in primary issuance must appear ONCE carrying both facets. The
// live quote wins the top-level price, but the sale price survives inside the
// issuance block — an earlier version dropped the launch entirely here.
func TestRWAOverview_OverlapKeepsBothFacets(t *testing.T) {
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
		t.Fatalf("assets = %d, want 1 (one asset, two facets — not duplicated)", len(assets))
	}
	a := assets[0].(map[string]any)
	if a["market"] != "secondary" {
		t.Errorf("market = %v, want secondary (live quote wins the price slot)", a["market"])
	}
	if a["price"] != 42.0 {
		t.Errorf("price = %v, want the live orderbook price 42", a["price"])
	}
	pi, ok := a["primary_issuance"].(map[string]any)
	if !ok {
		t.Fatalf("issuance facet was dropped for an overlapping asset: %v", a)
	}
	if pi["price"] != 100.0 {
		t.Errorf("primary_issuance.price = %v, want 100 — the sale price must survive", pi["price"])
	}
	if pi["total_bought"] != "6667" {
		t.Errorf("issuance progress lost: %v", pi["total_bought"])
	}
}

// A launch on the same token but a DIFFERENT quote is a different asset and must
// not be collapsed into the orderbook row — the merge key is {base}-{quote}, not
// the token address.
func TestRWAOverview_SameTokenDifferentQuoteAreDistinctAssets(t *testing.T) {
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "khbe", QuoteSymbol: "USDT", Source: prices.SourceEquiteez, TokenAddr: "KT1KHBE"},
	}
	eurl := khbeLaunch()
	eurl.QuoteSymbol = "eurl" // same token, other currency
	res := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("42"), TS: time.Now().UTC(), Found: true}},
	}
	r := overviewEngineWith(pairs, res, &stubLaunchLister{launches: []prices.RWALaunch{eurl}})
	body := getJSON(t, r, "/v1/rwa")

	assets := body["assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("assets = %d, want 2 (khbe-usdt and khbe-eurl are distinct)", len(assets))
	}
	got := []string{assets[0].(map[string]any)["symbol"].(string), assets[1].(map[string]any)["symbol"].(string)}
	if got[0] != "khbe-eurl" || got[1] != "khbe-usdt" {
		t.Errorf("symbols = %v, want [khbe-eurl khbe-usdt] (globally sorted)", got)
	}
}

// limit must be applied to the MERGED, globally sorted catalog. Previously the
// pairs were truncated first, so a primary asset could never appear once there
// were `limit` orderbook pairs — whatever its symbol.
func TestRWAOverview_LimitIsFairAcrossBothCatalogs(t *testing.T) {
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
		{ID: 2, BaseSymbol: "mars2", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
	}
	res := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("50"), TS: time.Now().UTC(), Found: true}},
	}
	// "khbe" sorts before both pairs, so with limit=2 it must make the cut.
	r := overviewEngineWith(pairs, res, &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}})

	body := getJSON(t, r, "/v1/rwa?limit=2")
	assets := body["assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(assets))
	}
	first := assets[0].(map[string]any)
	if first["symbol"] != "khbe-usdt" {
		t.Errorf("first symbol = %v, want khbe-usdt — limit must not starve the primary catalog", first["symbol"])
	}
	if second := assets[1].(map[string]any); second["symbol"] != "mars1-usdt" {
		t.Errorf("second symbol = %v, want mars1-usdt", second["symbol"])
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

// TestRWAOverview_PrimaryAssetConvertsIn is the regression guard for the `?in=`
// gap on the overview: a launch-only asset skipped conversion entirely, so a
// client asking for USD got only the native quote — while secondary rows in the
// SAME response carried their converted keys. The two market types must not
// differ on the wire here.
func TestRWAOverview_PrimaryAssetConvertsIn(t *testing.T) {
	// Both quote symbols must resolve to a registered Token, or the handler has
	// no FX source and drops every target regardless of the fix.
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
	})
	// One of each kind, so the assertion also pins that both convert alike.
	pairs := []prices.RWAPair{
		{ID: 1, BaseSymbol: "mars1", QuoteSymbol: "USDT", Source: prices.SourceEquiteez},
	}
	res := apiprices.ChangeRepoResult{
		Now: []apiprices.ChangeNow{{Currency: "usdt", Price: dec("50"), TS: time.Now().UTC(), Found: true}},
	}

	// rate 0.999: a rate != 1 proves a conversion ran instead of the native price
	// being echoed under the currency key. The SAME converter goes to the
	// ChartService, which validates its own — production wires both from
	// deps.FXConverter, and a nil one there rejects `?in=` before any asset is
	// built (the mini-series is FX-converted too).
	conv := &stubConverter{result: apiprices.ConversionResult{Rate: decimal.RequireFromString("0.999")}}
	gin.SetMode(gin.TestMode)
	deps := RWAOverviewDeps{
		Pairs:    &stubPairsLister{pairs: pairs},
		Launches: &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}},
		Change: &apiprices.ChangeService{
			Repo: &stubChangeRepo{res: res}, Cache: apiprices.NewChangeCache(), Kind: "rwa",
		},
		Charts: &apiprices.ChartService{
			Repo: &stubCandleRepo{}, Converter: conv,
			Caps: apiprices.DefaultCaps(), MaxLimit: 1000, Kind: "rwa",
		},
		Converter: conv,
		Source:    prices.SourceEquiteez,
	}
	r := gin.New()
	r.GET("/v1/rwa", deps.List())

	body := getJSON(t, r, "/v1/rwa?in=usd")

	byMarket := map[string]map[string]any{}
	for _, a := range body["assets"].([]any) {
		asset := a.(map[string]any)
		byMarket[asset["market"].(string)] = asset
	}
	primary, ok := byMarket["primary"]
	if !ok {
		t.Fatalf("no primary asset in response: %v", body["assets"])
	}
	// 100 × 0.999 = 99.9
	usd, ok := primary["usd"].(float64)
	if !ok {
		t.Fatalf("primary asset missing converted 'usd' key: %v", primary)
	}
	if usd < 99.89 || usd > 99.91 {
		t.Errorf("primary usd = %v, want ~99.9", usd)
	}
	// The secondary row in the same response must be unaffected: 50 × 0.999.
	if secondary, ok := byMarket["secondary"]; ok {
		if got, ok := secondary["usd"].(float64); !ok || got < 49.94 || got > 49.96 {
			t.Errorf("secondary usd = %v, want ~49.95", secondary["usd"])
		}
	}
}
