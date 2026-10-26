package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type snapshotRecorder struct{ saves [][]prices.RWAMarketSnapshot }

func (r *snapshotRecorder) SaveSnapshots(_ context.Context, rows []prices.RWAMarketSnapshot) error {
	r.saves = append(r.saves, rows)
	return nil
}

func TestCollectorWritesEmptySnapshotAndDiscardsGenerationChange(t *testing.T) {
	const generation = "2026-10-01T00:00:00Z"
	var empty, flip, changed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var data any
		if strings.Contains(req.Query, "quotesIndexerStatus") {
			g := generation
			if changed.Load() {
				g = "2026-10-02T00:00:00Z"
			}
			data = map[string]any{"dipdup_schema": []any{map[string]any{"name": "public", "created_at": g}},
				"dipdup_head":  []any{map[string]any{"timestamp": time.Now().UTC(), "updated_at": time.Now().UTC()}},
				"dipdup_index": []any{map[string]any{"name": "token_index", "status": "realtime"}}}
		} else {
			require.Contains(t, req.Query, "last_matched_price_timestamp")
			require.Contains(t, req.Query, "pricing_bids: orders(")
			require.Contains(t, req.Query, "pricing_asks: orders(")
			require.Contains(t, req.Query, "is_market_order: { _eq: false }")
			require.Contains(t, req.Query, "order_expiry: { _gt: $now }")
			require.Contains(t, req.Query, "unfulfilled_amount: { _gt: \"0\" }")
			require.Contains(t, req.Query, "order_by: [{ price_per_rwa_token: desc }, { id: asc }]")
			require.Contains(t, req.Query, "order_by: [{ price_per_rwa_token: asc }, { id: asc }]")
			require.NotEmpty(t, req.Variables["now"])
			require.NotContains(t, req.Query, "currencies(limit:", "all quote identities must be inspected")
			price := "1000000"
			if empty.Load() {
				price = "0"
			}
			data = map[string]any{"token": []any{map[string]any{"address": "asset", "token_id": 0, "orderbooks": []any{map[string]any{
				"address": "book", "highest_buy_price": price, "lowest_sell_price": price, "last_matched_price": price,
				"pricing_bids":                 []any{map[string]any{"order_type": 0, "price_per_rwa_token": price, "unfulfilled_amount": "1"}},
				"pricing_asks":                 []any{map[string]any{"order_type": 1, "price_per_rwa_token": price, "unfulfilled_amount": "1"}},
				"last_matched_price_timestamp": "2026-10-01T12:00:00Z", "currencies": []any{map[string]any{"currency_name": "usdt", "token": map[string]any{"address": "quote", "token_id": 0}}}}}}}}
			if flip.Load() {
				changed.Store(true)
			}
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data}))
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.API.TimeoutSeconds = 5
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	cfg.Equiteez.IndexerURL = server.URL
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	prices.RegisterTokens([]prices.TokenInfo{{Symbol: "usdt", Decimals: 6, Enabled: true}})
	history := &stubPriceRepo{}
	snapshots := &snapshotRecorder{}
	job := NewEquiteezRWAJob(cfg, history, nil, nil).WithSnapshots(snapshots)
	pairs := []prices.RWAPair{{ID: 1, Source: prices.SourceEquiteez, TokenAddr: "asset", QuoteAddr: "quote", OrderbookAddr: "book", QuoteSymbol: "usdt", IndexerGeneration: generation}}
	require.NoError(t, job.collectOnce(context.Background(), pairs))
	require.Len(t, snapshots.saves, 1)
	require.NotNil(t, snapshots.saves[0][0].Bid)
	require.Equal(t, 1, history.saveCount())
	empty.Store(true)
	require.NoError(t, job.collectOnce(context.Background(), pairs))
	require.Len(t, snapshots.saves, 2)
	require.Nil(t, snapshots.saves[1][0].Bid)
	require.Nil(t, snapshots.saves[1][0].Ask)
	require.Equal(t, 1, history.saveCount(), "empty ticks clear latest snapshot without inventing historical zeroes")
	flip.Store(true)
	require.Error(t, job.collectOnce(context.Background(), pairs))
	require.Len(t, snapshots.saves, 2, "changed generation must not publish even an empty snapshot")
}

func marketTokens(t *testing.T) []equiteez.TokenWithOrderbooks {
	t.Helper()
	var tokens []equiteez.TokenWithOrderbooks
	require.NoError(t, json.Unmarshal([]byte(`[{"address":"KT1asset","token_id":0,"orderbooks":[{
		"address":"KT1book","highest_buy_price":"9007199254740993","lowest_sell_price":"9007199254740995",
		"last_matched_price":"100000000","last_matched_price_timestamp":"2026-10-01T12:00:00Z",
        "pricing_bids":[{"order_type":0,"price_per_rwa_token":"9007199254740993","unfulfilled_amount":"1"}],
        "pricing_asks":[{"order_type":1,"price_per_rwa_token":"9007199254740995","unfulfilled_amount":"1"}],
		"currencies":[{"currency_name":"USDT","token":{"address":"KT1quote","token_id":0}}]}]}]`), &tokens))
	return tokens
}

func TestMarketSnapshotsExactPriceAndExecutionTime(t *testing.T) {
	tokens := marketTokens(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pair := prices.RWAPair{ID: 1, TokenAddr: "KT1asset", QuoteAddr: "KT1quote", OrderbookAddr: "KT1book", QuoteSymbol: "usdt"}
	rows := buildMarketSnapshots(tokens, []prices.RWAPair{pair}, map[int64]int{1: 6}, "basenet", "generation", now)
	require.Len(t, rows, 1)
	s := rows[0]
	require.Empty(t, s.Reason)
	require.Equal(t, "9007199254.740993", s.Bid.String())
	require.Equal(t, "9007199254.740995", s.Ask.String())
	require.Equal(t, "100", s.LastPrice.String())
	require.Equal(t, "2026-10-01T12:00:00Z", s.LastTradeAt.Format(time.RFC3339))
	require.Equal(t, now, s.ObservedAt)
	require.Equal(t, 0, *s.TokenID)
	require.Equal(t, 0, *s.QuoteTokenID)
}

func TestMarketSnapshotsClearMissingSidesAndRejectAmbiguousIdentity(t *testing.T) {
	pair := prices.RWAPair{ID: 1, TokenAddr: "KT1asset", QuoteAddr: "KT1quote", OrderbookAddr: "KT1book", QuoteSymbol: "usdt"}
	for _, tc := range []struct {
		name, reason string
		change       func([]equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks
	}{
		{"one side", "", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks[0].PricingAsks = nil
			return ts
		}},
		{"empty", "", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks[0].PricingAsks = nil
			ts[0].Orderbooks[0].PricingBids = nil
			ts[0].Orderbooks[0].LastMatchedPrice = equiteez.FlexibleFloat{}
			return ts
		}},
		{"missing", "orderbook_missing", func(_ []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks { return nil }},
		{"multiple quotes", "ambiguous_quote", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			book := &ts[0].Orderbooks[0]
			book.Currencies = append(book.Currencies, book.Currencies[0])
			return ts
		}},
		{"wrong quote", "quote_identity_mismatch", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks[0].Currencies[0].Token.Address = "wrong"
			return ts
		}},
		{"unknown quote id", "quote_identity_mismatch", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks[0].Currencies[0].Token.TokenIDKnown = false
			return ts
		}},
		{"nonzero quote id", "quote_identity_mismatch", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks[0].Currencies[0].Token.TokenID = 1
			return ts
		}},
		{"nonzero base id", "unsupported_token_id", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks { ts[0].TokenID = 1; return ts }},
		{"unknown base id", "unsupported_token_id", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].TokenIDKnown = false
			return ts
		}},
		{"duplicate book", "ambiguous_identity", func(ts []equiteez.TokenWithOrderbooks) []equiteez.TokenWithOrderbooks {
			ts[0].Orderbooks = append(ts[0].Orderbooks, ts[0].Orderbooks[0])
			return ts
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := buildMarketSnapshots(tc.change(marketTokens(t)), []prices.RWAPair{pair}, map[int64]int{1: 6}, "basenet", "generation", time.Now())
			require.Len(t, rows, 1)
			require.Equal(t, tc.reason, rows[0].Reason)
			if tc.reason != "" || tc.name == "empty" {
				require.Nil(t, rows[0].Bid)
				require.Nil(t, rows[0].LastPrice)
			}
			require.Nil(t, rows[0].Ask)
		})
	}
}

func TestMarketSnapshotsUseOnlyLiveLimitsAndIgnoreSellCurrencyLabel(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	pair := prices.RWAPair{ID: 1, TokenAddr: "KT1asset", QuoteAddr: "KT1quote", OrderbookAddr: "KT1book", QuoteSymbol: "usdt"}
	tokens := marketTokens(t)
	book := &tokens[0].Orderbooks[0]
	book.Currencies = append(book.Currencies, equiteez.OrderbookCurrency{CurrencyName: "USD"})
	book.PricingBids[0].ExpiresAt = &deadline
	book.HighestBuyPrice = equiteez.FlexibleFloatFromDecimal(decimal.NewFromInt(999999))
	collect := func() prices.RWAMarketSnapshot {
		return buildMarketSnapshots(tokens, []prices.RWAPair{pair}, map[int64]int{1: 6}, "basenet", "g", now)[0]
	}
	got := collect()
	require.Empty(t, got.Reason)
	require.Equal(t, "9007199254.740993", got.Bid.String(), "stored contract best price is not the live-limit estimate")
	require.Equal(t, &deadline, got.BidExpiresAt)
	for _, tc := range []struct {
		name   string
		change func(*equiteez.PricingLimitOrder)
	}{
		{"market protection price", func(o *equiteez.PricingLimitOrder) { o.Market = true }},
		{"expired in flight", func(o *equiteez.PricingLimitOrder) { o.ExpiresAt = &now }},
		{"canceled", func(o *equiteez.PricingLimitOrder) { o.Canceled = true }},
		{"refunded", func(o *equiteez.PricingLimitOrder) { o.Refunded = true }},
		{"fulfilled", func(o *equiteez.PricingLimitOrder) { o.Fulfilled = true }},
		{"expired flag", func(o *equiteez.PricingLimitOrder) { o.Expired = true }},
		{"zero remainder", func(o *equiteez.PricingLimitOrder) { o.Remaining = equiteez.FlexibleFloat{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := book.PricingBids[0]
			tc.change(&book.PricingBids[0])
			got := collect()
			require.Nil(t, got.Bid)
			require.Nil(t, got.BidExpiresAt)
			require.NotNil(t, got.LastPrice, "eligibility affects midpoint only")
			book.PricingBids[0] = original
		})
	}
	book.Currencies = append(book.Currencies, equiteez.OrderbookCurrency{CurrencyName: "UNKNOWN"})
	require.Equal(t, "ambiguous_quote", collect().Reason, "unresolved actual currencies are never ignored")
}
