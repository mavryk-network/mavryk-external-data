package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type stubQueryService struct {
	points []prices.PricePoint
	err    error
	gotQ   prices.Query
}

func (s *stubQueryService) Query(_ context.Context, q prices.Query) ([]prices.PricePoint, error) {
	s.gotQ = q
	if s.err != nil {
		return nil, s.err
	}
	return s.points, nil
}

func newTestEngine(t *testing.T, deps TokenPriceDeps) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/prices/:token", deps.ListByToken())
	r.GET("/v1/prices/:token/latest", deps.LatestSnapshot())
	return r
}

func registerTestTokens(t *testing.T) {
	t.Helper()
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "mvrk", Name: "Mavryk", Enabled: true, CoinGeckoID: "mavryk-network"},
	})
}

func TestListByToken_UnknownToken_404(t *testing.T) {
	registerTestTokens(t)
	deps := TokenPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      1000,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/unknown", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestListByToken_BadCurrency_400(t *testing.T) {
	registerTestTokens(t)
	deps := TokenPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      1000,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk?currency=zzz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown currency", w.Code)
	}
}

func TestListByToken_LimitExceedsMax_400(t *testing.T) {
	registerTestTokens(t)
	deps := TokenPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      100,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk?limit=999999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for limit overflow", w.Code)
	}
}

func TestListByToken_OK_ReturnsArray(t *testing.T) {
	registerTestTokens(t)
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	stub := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now, Metric: "usd", Price: decimal.NewFromFloat(1.5)},
		},
	}
	deps := TokenPriceDeps{
		Service:       stub,
		DefaultSource: prices.SourceCoinGecko,
		MaxLimit:      1000,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk?currency=usd&limit=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v\n%s", err, w.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0]["currency"] != "usd" {
		t.Errorf("currency = %v, want usd", out[0]["currency"])
	}
	if _, ok := out[0]["price"].(float64); !ok {
		t.Errorf("price type = %T, want float64 (JSON number); body=%s", out[0]["price"], w.Body.String())
	}
	// Validate the query reached the service correctly.
	if stub.gotQ.EntityKey != "mvrk" || stub.gotQ.Source != prices.SourceCoinGecko {
		t.Errorf("query EntityKey/Source = %q/%q", stub.gotQ.EntityKey, stub.gotQ.Source)
	}
	if len(stub.gotQ.Metrics) != 1 || stub.gotQ.Metrics[0] != "usd" {
		t.Errorf("query Metrics = %v, want [usd]", stub.gotQ.Metrics)
	}
}

func TestLatestSnapshot_NoData_404(t *testing.T) {
	registerTestTokens(t)
	deps := TokenPriceDeps{
		Service:       &stubQueryService{}, // empty
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestLatestSnapshot_Transposed(t *testing.T) {
	registerTestTokens(t)
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	stub := &stubQueryService{
		points: []prices.PricePoint{
			{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now, Metric: "usd", Price: decimal.NewFromFloat(1.5)},
			{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Timestamp: now, Metric: "eur", Price: decimal.NewFromFloat(1.4)},
		},
	}
	deps := TokenPriceDeps{
		Service:       stub,
		DefaultSource: prices.SourceCoinGecko,
	}
	r := newTestEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/prices/mvrk/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var snap struct {
		Timestamp string             `json:"timestamp"`
		Values    map[string]float64 `json:"values"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode body: %v\n%s", err, w.Body.String())
	}
	if snap.Timestamp == "" {
		t.Errorf("timestamp missing in snapshot; body=%s", w.Body.String())
	}
	if snap.Values["usd"] != 1.5 {
		t.Errorf("values.usd = %v, want 1.5", snap.Values["usd"])
	}
	if snap.Values["eur"] != 1.4 {
		t.Errorf("values.eur = %v, want 1.4", snap.Values["eur"])
	}
	// source and entity must not be present.
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	for _, badKey := range []string{"source", "entity"} {
		if _, present := raw[badKey]; present {
			t.Errorf("unexpected key %q in snapshot response", badKey)
		}
	}
}
