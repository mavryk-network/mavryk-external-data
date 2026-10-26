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

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type handlerSnapshot struct{ row prices.RWAMarketSnapshot }

func (s handlerSnapshot) Snapshot(context.Context, int64) (prices.RWAMarketSnapshot, bool, error) {
	return s.row, true, nil
}

func TestRWALatestBeforeFirstTradeIncludesPricing(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	zero := 0
	bid, ask := decimal.NewFromInt(90), decimal.NewFromInt(110)
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, TokenAddr: "asset", QuoteAddr: "quote", OrderbookAddr: "book", BaseSymbol: "rwa", QuoteSymbol: "usdt", IndexerGeneration: "g"}
	deps := RWAPriceDeps{Lookup: &stubLookup{pair: pair}, Service: &stubQueryService{}, DefaultSource: prices.SourceEquiteez,
		Pricing: &apiprices.RWAPricingService{Network: "basenet", Now: func() time.Time { return now }, Snapshots: handlerSnapshot{row: prices.RWAMarketSnapshot{
			PairID: 1, Network: "basenet", IndexerGeneration: "g", TokenAddr: "asset", TokenID: &zero, QuoteAddr: "quote", QuoteTokenID: &zero,
			OrderbookAddr: "book", NativeQuote: "usdt", ObservedAt: now, Bid: &bid, Ask: &ask}}}}
	r := newRWAEngine(t, deps)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/rwa-usdt/latest", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Nil(t, out["price"])
	require.Nil(t, out["timestamp"])
	require.Equal(t, "secondary", out["market"])
	pricing := out["pricing"].(map[string]any)
	require.Equal(t, "0", pricing["token_id"])
	market := pricing["market_price"].(map[string]any)
	sale := pricing["last_sale"].(map[string]any)
	require.Equal(t, "100", market["price"])
	require.Nil(t, market["usd"])
	require.Equal(t, "missing", market["fx_status"])
	require.Equal(t, "no_trade", sale["reason"])
	require.Nil(t, sale["executed_at"])
	require.Nil(t, pricing["primary_issuance"])
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/rwa-usdt", nil))
	require.Equal(t, 200, w.Code)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, out, rows[0])
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/rwa-usdt/pricing", nil))
	require.Equal(t, 404, w.Code)
}

type handlerHistory struct {
	row   prices.RWAMarketSnapshot
	err   error
	calls int
}

func (s *handlerHistory) Snapshot(context.Context, int64) (prices.RWAMarketSnapshot, bool, error) {
	return s.row, true, s.err
}
func (s *handlerHistory) SnapshotsAt(context.Context, int64, []time.Time) ([]prices.RWAMarketSnapshot, error) {
	s.calls++
	return []prices.RWAMarketSnapshot{s.row}, s.err
}
func TestRWAHistoryPricingAndOptionalReadFailure(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-time.Hour).Add(123456 * time.Microsecond)
	trade := observed.Add(-time.Minute)
	zero := 0
	bid, ask, last := decimal.NewFromInt(90), decimal.NewFromInt(110), decimal.NewFromInt(95)
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, TokenAddr: "asset", QuoteAddr: "quote", OrderbookAddr: "book", BaseSymbol: "rwa", QuoteSymbol: "usdt", IndexerGeneration: "now-generation"}
	archive := &handlerHistory{row: prices.RWAMarketSnapshot{PairID: 1, Network: "basenet", IndexerGeneration: "old-generation", TokenAddr: "asset", TokenID: &zero, QuoteAddr: "quote", QuoteTokenID: &zero, OrderbookAddr: "book", NativeQuote: "usdt", ObservedAt: observed, Bid: &bid, Ask: &ask, LastPrice: &last, LastTradeAt: &trade}}
	point := prices.PricePoint{Metric: "last", Timestamp: observed, Price: last}
	deps := RWAPriceDeps{Lookup: &stubLookup{pair: pair}, Service: &stubQueryService{points: []prices.PricePoint{point, {Metric: "last", Timestamp: observed.Add(time.Second), Price: last}}}, DefaultSource: prices.SourceEquiteez,
		Pricing: &apiprices.RWAPricingService{Network: "basenet", Now: func() time.Time { return now }, Snapshots: archive}}
	engine := newRWAEngine(t, deps)
	path := "/v1/rwa/rwa-usdt?from=2026-10-01T00:00:00Z"
	read := func(path string) []map[string]any {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, w.Code, w.Body.String())
		var rows []map[string]any
		if path == "/v1/rwa/rwa-usdt/latest" {
			var row map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &row))
			return []map[string]any{row}
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &rows))
		return rows
	}
	rows := read(path)
	require.Equal(t, 1, archive.calls)
	require.Len(t, rows, 2)
	require.Equal(t, float64(95), rows[0]["price"])
	require.Equal(t, "2026-10-26T11:00:00Z", rows[0]["timestamp"])
	q := rows[0]["pricing"].(map[string]any)
	require.Equal(t, observed.Format(time.RFC3339Nano), q["observed_at"])
	market := q["market_price"].(map[string]any)
	require.Equal(t, "100", market["price"])
	require.Equal(t, "not_requested", market["fx_status"])
	require.Nil(t, q["primary_issuance"])
	require.Nil(t, rows[1]["pricing"])
	archive.err = errors.New("optional snapshot table unavailable")
	rows = read(path)
	require.Equal(t, float64(95), rows[0]["price"])
	require.Nil(t, rows[0]["pricing"])
	latest := read("/v1/rwa/rwa-usdt/latest")[0]
	require.Equal(t, float64(95), latest["price"])
	require.Nil(t, latest["pricing"])
}
