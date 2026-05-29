package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// --- stubs ---

type stubTickerService struct {
	snap    tickers.Snapshot
	snapErr error
	dist    tickers.Distribution
	distErr error
	latestQ apitickers.LatestQuery
	distQ   apitickers.DistributionQuery
}

func (s *stubTickerService) LatestSnapshot(_ context.Context, q apitickers.LatestQuery) (tickers.Snapshot, error) {
	s.latestQ = q
	if s.snapErr != nil {
		return tickers.Snapshot{}, s.snapErr
	}
	return s.snap, nil
}

func (s *stubTickerService) VolumeDistribution(_ context.Context, q apitickers.DistributionQuery) (tickers.Distribution, error) {
	s.distQ = q
	if s.distErr != nil {
		return tickers.Distribution{}, s.distErr
	}
	return s.dist, nil
}

type stubTickerConverter struct {
	results map[string]apiprices.ConversionResult
	errors  map[string]error
}

func (s *stubTickerConverter) Convert(
	_ context.Context,
	src prices.Token,
	target prices.Currency,
	amount decimal.Decimal,
	ts time.Time,
) (apiprices.ConversionResult, error) {
	key := string(src) + "->" + string(target)
	if err, ok := s.errors[key]; ok {
		return apiprices.ConversionResult{}, err
	}
	if r, ok := s.results[key]; ok {
		// scale amount × rate so output looks like a real conversion
		r.Amount = amount.Mul(r.Rate)
		if r.RateTS.IsZero() {
			r.RateTS = ts
		}
		return r, nil
	}
	return apiprices.ConversionResult{}, apiprices.ErrNoFXRate
}

func registerTickerTestTokens(t *testing.T) {
	t.Helper()
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "mvrk", Name: "Mavryk", Enabled: true, CoinGeckoID: "mavryk-network"},
		{Symbol: "btc", Name: "Bitcoin", Enabled: true, CoinGeckoID: "bitcoin"},
		{Symbol: "usdt", Name: "Tether", Enabled: true, CoinGeckoID: "tether"},
	})
}

func newTickerEngine(deps TickerDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/tickers/:token/latest", deps.LatestByToken())
	r.GET("/v1/tickers/:token/distribution", deps.Distribution())
	return r
}

// --- /latest tests ---

func TestLatestByToken_HappyPath(t *testing.T) {
	registerTickerTestTokens(t)
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	change := decimal.RequireFromString("5.34")
	vol := decimal.RequireFromString("1234567.89")
	snap := tickers.Snapshot{
		Token: prices.Token("mvrk"), Source: prices.SourceCoinGecko, Timestamp: now,
		Rows: []tickers.SnapshotRow{
			{
				Exchange:     tickers.Exchange{ID: "binance", Name: "Binance", Kind: tickers.ExchangeKindCEX, LogoURL: "https://x/b.png"},
				TargetSymbol: "btc",
				Timestamp:    now,
				LastPrice:    decimal.RequireFromString("0.000021"),
				VolumeBase:   &vol,
				Change24hPct: &change,
				TradeURL:     "https://binance.com/trade",
				IsStale:      false,
			},
		},
	}
	deps := TickerDeps{
		Service:          &stubTickerService{snap: snap},
		DefaultSource:    prices.SourceCoinGecko,
		TickerStaleAfter: time.Hour,
	}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got TickersSnapshotDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Entity != "mvrk" || got.Source != "coingecko" {
		t.Errorf("envelope: %+v", got)
	}
	if len(got.Tickers) != 1 {
		t.Fatalf("tickers = %d", len(got.Tickers))
	}
	row := got.Tickers[0]
	if row.Exchange != "binance" || row.Target != "btc" || row.Pair != "MVRK/BTC" {
		t.Errorf("row keys: %+v", row)
	}
	if row.LogoURL != "https://x/b.png" || row.ExchangeKind != "cex" {
		t.Errorf("row exchange meta: %+v", row)
	}
	if row.LastPrice != "0.000021" || row.Change24hPct == nil || *row.Change24hPct != "5.34" {
		t.Errorf("numbers: %+v", row)
	}
}

func TestLatestByToken_UnknownToken_404(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{Service: &stubTickerService{}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/ghost/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d want 404 body=%s", w.Code, w.Body.String())
	}
}

func TestLatestByToken_BadInCurrency_400(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{
		Service:          &stubTickerService{snap: tickers.Snapshot{Token: "mvrk"}},
		Converter:        &stubTickerConverter{},
		DefaultSource:    prices.SourceCoinGecko,
		MaxInCurrencies:  10,
		TickerStaleAfter: time.Hour,
	}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest?in=zzz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d want 400 body=%s", w.Code, w.Body.String())
	}
}

func TestLatestByToken_InMissingConverter_400(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{
		Service:          &stubTickerService{},
		Converter:        nil,
		DefaultSource:    prices.SourceCoinGecko,
		TickerStaleAfter: time.Hour,
	}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest?in=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d want 400 (converter unavailable)", w.Code)
	}
}

func TestLatestByToken_EmptySnapshot_200(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{Service: &stubTickerService{snap: tickers.Snapshot{Token: "mvrk"}}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	var got TickersSnapshotDTO
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Tickers) != 0 {
		t.Errorf("tickers = %d want 0", len(got.Tickers))
	}
}

func TestLatestByToken_IncludeStaleQuery_PassedThrough(t *testing.T) {
	registerTickerTestTokens(t)
	stub := &stubTickerService{snap: tickers.Snapshot{Token: "mvrk"}}
	deps := TickerDeps{Service: stub, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest?include_stale=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !stub.latestQ.IncludeStale {
		t.Errorf("IncludeStale flag not propagated to service")
	}
}

func TestLatestByToken_WithInConvertsRowVolume(t *testing.T) {
	registerTickerTestTokens(t)
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	vol := decimal.RequireFromString("1000")
	snap := tickers.Snapshot{
		Token: "mvrk", Source: prices.SourceCoinGecko, Timestamp: now,
		Rows: []tickers.SnapshotRow{{
			Exchange:     tickers.Exchange{ID: "binance", Name: "Binance", Kind: tickers.ExchangeKindCEX},
			TargetSymbol: "btc",
			Timestamp:    now,
			LastPrice:    decimal.RequireFromString("0.000021"),
			VolumeBase:   &vol,
		}},
	}
	converter := &stubTickerConverter{results: map[string]apiprices.ConversionResult{
		"btc->usd":  {Rate: decimal.RequireFromString("60000"), Source: prices.SourceCoinGecko},
		"mvrk->usd": {Rate: decimal.RequireFromString("0.05"), Source: prices.SourceCoinGecko},
	}}
	deps := TickerDeps{
		Service:          &stubTickerService{snap: snap},
		Converter:        converter,
		DefaultSource:    prices.SourceCoinGecko,
		MaxInCurrencies:  10,
		TickerStaleAfter: time.Hour,
	}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/latest?in=usd", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got TickersSnapshotDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tickers) != 1 {
		t.Fatalf("tickers = %d", len(got.Tickers))
	}
	block, ok := got.Tickers[0].In["usd"]
	if !ok {
		t.Fatalf("in.usd missing: %+v", got.Tickers[0])
	}
	// price = 0.000021 × 60000 = 1.26 (btc → usd)
	if block.Price != "1.26" {
		t.Errorf("price = %s want 1.26", block.Price)
	}
	// volume = 1000 × 0.05 = 50 (mvrk → usd)
	if block.Volume24h == nil || *block.Volume24h != "50" {
		t.Errorf("volume = %v want 50", block.Volume24h)
	}
}

// --- /distribution tests ---

func TestDistribution_HappyByExchange(t *testing.T) {
	registerTickerTestTokens(t)
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	dist := tickers.Distribution{
		Token: "mvrk", Source: prices.SourceCoinGecko, Timestamp: now, GroupBy: tickers.GroupByExchange,
		Total: decimal.RequireFromString("2000"),
		Rows: []tickers.DistributionRow{
			{Exchange: tickers.Exchange{ID: "binance", Name: "Binance", Kind: tickers.ExchangeKindCEX},
				VolumeBase: decimal.RequireFromString("1500"), SharePct: decimal.RequireFromString("75")},
			{Exchange: tickers.Exchange{ID: "kraken", Name: "Kraken", Kind: tickers.ExchangeKindCEX},
				VolumeBase: decimal.RequireFromString("500"), SharePct: decimal.RequireFromString("25")},
		},
	}
	deps := TickerDeps{Service: &stubTickerService{dist: dist}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/distribution?group_by=exchange", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got DistributionDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.GroupBy != "exchange" || got.TotalVolumeBase != "2000" || len(got.Rows) != 2 {
		t.Errorf("envelope: %+v", got)
	}
	if got.Rows[0].Exchange != "binance" || got.Rows[0].SharePct != "75" {
		t.Errorf("row0: %+v", got.Rows[0])
	}
}

func TestDistribution_HappyByTarget(t *testing.T) {
	registerTickerTestTokens(t)
	dist := tickers.Distribution{
		Token: "mvrk", Source: prices.SourceCoinGecko, GroupBy: tickers.GroupByTarget,
		Total: decimal.RequireFromString("1000"),
		Rows: []tickers.DistributionRow{
			{TargetSymbol: "btc", VolumeBase: decimal.RequireFromString("600"), SharePct: decimal.RequireFromString("60")},
			{TargetSymbol: "usdt", VolumeBase: decimal.RequireFromString("400"), SharePct: decimal.RequireFromString("40")},
		},
	}
	deps := TickerDeps{Service: &stubTickerService{dist: dist}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/distribution?group_by=target", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got DistributionDTO
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got.GroupBy != "target" || len(got.Rows) != 2 {
		t.Errorf("got: %+v", got)
	}
	if got.Rows[0].Target != "btc" || got.Rows[0].Exchange != "" {
		t.Errorf("by-target should not set exchange: %+v", got.Rows[0])
	}
}

func TestDistribution_BadGroupBy_400(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{Service: &stubTickerService{}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	for _, val := range []string{"", "foo", "by_exchange"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/distribution?group_by="+val, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("group_by=%q: status = %d want 400", val, w.Code)
		}
	}
}

func TestDistribution_StaleFenceAlwaysApplied(t *testing.T) {
	registerTickerTestTokens(t)
	stub := &stubTickerService{dist: tickers.Distribution{Token: "mvrk", GroupBy: tickers.GroupByExchange}}
	deps := TickerDeps{Service: stub, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: 90 * time.Minute}
	r := newTickerEngine(deps)
	// Even if a caller tries `?include_stale=true`, /distribution must always
	// fence by StaleAfter (the handler doesn't read include_stale on this route).
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/mvrk/distribution?group_by=exchange&include_stale=true", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if stub.distQ.StaleAfter != 90*time.Minute {
		t.Errorf("StaleAfter = %v want 90m (forced by deps)", stub.distQ.StaleAfter)
	}
}

func TestDistribution_UnknownToken_404(t *testing.T) {
	registerTickerTestTokens(t)
	deps := TickerDeps{Service: &stubTickerService{}, DefaultSource: prices.SourceCoinGecko, TickerStaleAfter: time.Hour}
	r := newTickerEngine(deps)
	req := httptest.NewRequest(http.MethodGet, "/v1/tickers/ghost/distribution?group_by=exchange", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d want 404", w.Code)
	}
}
