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

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type handlerSnapshot struct{ row prices.RWAMarketSnapshot }

func (s handlerSnapshot) Snapshot(context.Context, int64) (prices.RWAMarketSnapshot, bool, error) {
	return s.row, true, nil
}

func TestRWAPricingBeforeFirstTradeAndLegacy404(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	zero := 0
	bid, ask := decimal.NewFromInt(90), decimal.NewFromInt(110)
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, TokenAddr: "asset", QuoteAddr: "quote", OrderbookAddr: "book", BaseSymbol: "rwa", QuoteSymbol: "usdt", IndexerGeneration: "g"}
	deps := RWAPriceDeps{Lookup: &stubLookup{pair: pair}, Service: &stubQueryService{}, DefaultSource: prices.SourceEquiteez,
		Pricing: &apiprices.RWAPricingService{Network: "basenet", Now: func() time.Time { return now }, Snapshots: handlerSnapshot{row: prices.RWAMarketSnapshot{
			PairID: 1, Network: "basenet", IndexerGeneration: "g", TokenAddr: "asset", TokenID: &zero, QuoteAddr: "quote", QuoteTokenID: &zero,
			OrderbookAddr: "book", NativeQuote: "usdt", ObservedAt: now, Bid: &bid, Ask: &ask}}}}
	r := newRWAEngine(t, deps)
	r.GET("/v1/rwa/:symbol/pricing", deps.PricingBySymbol())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/rwa-usdt/pricing", nil))
	require.Equal(t, 200, w.Code, w.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	require.Equal(t, "0", out["token_id"])
	market := out["market_price"].(map[string]any)
	sale := out["last_sale"].(map[string]any)
	require.Equal(t, "100", market["price"])
	require.Nil(t, market["usd"])
	require.Equal(t, "missing", market["fx_status"])
	require.Equal(t, "no_trade", sale["reason"])
	require.Nil(t, sale["executed_at"])
	require.Nil(t, out["primary_issuance"])
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rwa/rwa-usdt/latest", nil))
	require.Equal(t, 404, w.Code)
}
