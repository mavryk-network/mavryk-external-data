package coingecko

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}

func ptrDec(s string) *decimal.Decimal {
	d := dec(s)
	return &d
}

func TestMapToTickers_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	resp := &TickersResponse{
		Tickers: []TickerEntry{
			{
				Base:   "MVRK",
				Target: "BTC",
				Market: TickerMarket{
					Name: "Binance", Identifier: "binance",
					Logo: "https://example/binance.png", HasTradingIncentive: false,
				},
				Last:                   ptrDec("0.000021"),
				Volume:                 ptrDec("1234567.89"),
				BidAskSpreadPercentage: ptrDec("0.1"),
				TrustScore:             "green",
				TradeURL:               "https://binance.com/trade",
				LastFetchAt:            "2026-05-29T11:55:00Z",
				LastTradedAt:           "2026-05-29T11:54:00Z",
			},
		},
	}
	ex, rows := MapToTickers(resp, prices.SourceCoinGecko, prices.Token("mvrk"), now)
	if len(ex) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(ex))
	}
	if ex[0].ID != "binance" || ex[0].Name != "Binance" || ex[0].LogoURL != "https://example/binance.png" {
		t.Errorf("exchange: %+v", ex[0])
	}
	if ex[0].Kind != tickers.ExchangeKindCEX {
		t.Errorf("kind = %q want cex", ex[0].Kind)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d want 1", len(rows))
	}
	r := rows[0]
	if r.Token != "mvrk" || r.Source != prices.SourceCoinGecko || r.ExchangeID != "binance" || r.TargetSymbol != "btc" {
		t.Errorf("row keys: %+v", r)
	}
	if !r.LastPrice.Equal(dec("0.000021")) {
		t.Errorf("last_price = %s", r.LastPrice)
	}
	if r.VolumeBase == nil || !r.VolumeBase.Equal(dec("1234567.89")) {
		t.Errorf("volume = %v", r.VolumeBase)
	}
	if r.Timestamp != time.Date(2026, 5, 29, 11, 55, 0, 0, time.UTC) {
		t.Errorf("ts = %v want last_fetch_at", r.Timestamp)
	}
	if r.TrustScore != "green" || r.TradeURL != "https://binance.com/trade" {
		t.Errorf("trust/trade: %s %s", r.TrustScore, r.TradeURL)
	}
}

func TestMapToTickers_SkipMissingIdentifier(t *testing.T) {
	resp := &TickersResponse{
		Tickers: []TickerEntry{
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "", Name: "Phantom"}, Last: ptrDec("1")},
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "kraken", Name: "Kraken"}, Last: ptrDec("1")},
		},
	}
	ex, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(ex) != 1 || ex[0].ID != "kraken" {
		t.Errorf("ex = %+v", ex)
	}
	if len(rows) != 1 || rows[0].ExchangeID != "kraken" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapToTickers_SkipMissingLast(t *testing.T) {
	resp := &TickersResponse{
		Tickers: []TickerEntry{
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "a"}, Last: nil},
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "b"}, Last: ptrDec("0")},
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "c"}, Last: ptrDec("-1")},
			{Base: "MVRK", Target: "USDT", Market: TickerMarket{Identifier: "d"}, Last: ptrDec("1")},
		},
	}
	_, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(rows) != 1 || rows[0].ExchangeID != "d" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapToTickers_SkipWrongBaseToken(t *testing.T) {
	resp := &TickersResponse{
		Tickers: []TickerEntry{
			{Base: "BTC", Target: "MVRK", Market: TickerMarket{Identifier: "x"}, Last: ptrDec("0.5")},
			{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "y"}, Last: ptrDec("0.5")},
		},
	}
	_, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(rows) != 1 || rows[0].ExchangeID != "y" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapToTickers_TimestampFallback(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		entry     TickerEntry
		wantTS    time.Time
		wantIsNow bool
	}{
		{
			"last_fetch_at preferred",
			TickerEntry{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "a"},
				Last: ptrDec("1"), LastFetchAt: "2026-05-29T11:30:00Z", LastTradedAt: "2026-05-29T10:00:00Z", Timestamp: "2026-05-29T09:00:00Z"},
			time.Date(2026, 5, 29, 11, 30, 0, 0, time.UTC), false,
		},
		{
			"timestamp fallback",
			TickerEntry{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "b"},
				Last: ptrDec("1"), Timestamp: "2026-05-29T09:00:00Z"},
			time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC), false,
		},
		{
			"last_traded_at fallback",
			TickerEntry{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "c"},
				Last: ptrDec("1"), LastTradedAt: "2026-05-29T08:00:00Z"},
			time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC), false,
		},
		{
			"all empty → now",
			TickerEntry{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "d"}, Last: ptrDec("1")},
			now, true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &TickersResponse{Tickers: []TickerEntry{c.entry}}
			_, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", now)
			if len(rows) != 1 {
				t.Fatalf("rows = %d", len(rows))
			}
			if !rows[0].Timestamp.Equal(c.wantTS) {
				t.Errorf("ts = %v want %v", rows[0].Timestamp, c.wantTS)
			}
		})
	}
}

func TestMapToTickers_EmptyAndNil(t *testing.T) {
	if ex, rows := MapToTickers(nil, prices.SourceCoinGecko, "mvrk", time.Now()); ex != nil || rows != nil {
		t.Errorf("nil resp: ex=%v rows=%v", ex, rows)
	}
	if ex, rows := MapToTickers(&TickersResponse{}, prices.SourceCoinGecko, "mvrk", time.Now()); ex != nil || rows != nil {
		t.Errorf("empty resp: ex=%v rows=%v", ex, rows)
	}
}

func TestMapToTickers_StaleFlagPromotesAnomaly(t *testing.T) {
	resp := &TickersResponse{Tickers: []TickerEntry{
		{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "x"}, Last: ptrDec("1"), IsStale: true},
	}}
	_, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(rows) != 1 || !rows[0].IsAnomaly {
		t.Errorf("is_stale should promote is_anomaly: rows=%+v", rows)
	}
}

func TestMapToTickers_ExchangeDeduped(t *testing.T) {
	// MVRK/BTC + MVRK/USDT on Binance — same exchange row, two ticker rows.
	resp := &TickersResponse{Tickers: []TickerEntry{
		{Base: "mvrk", Target: "btc", Market: TickerMarket{Identifier: "binance", Name: "Binance"}, Last: ptrDec("1")},
		{Base: "mvrk", Target: "usdt", Market: TickerMarket{Identifier: "binance", Name: "Binance"}, Last: ptrDec("2")},
	}}
	ex, rows := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(ex) != 1 {
		t.Errorf("exchanges = %d want 1 (deduped by id)", len(ex))
	}
	if len(rows) != 2 {
		t.Errorf("rows = %d want 2", len(rows))
	}
	// Sanity: both rows reference the same exchange id.
	if rows[0].ExchangeID != "binance" || rows[1].ExchangeID != "binance" {
		t.Errorf("rows: %+v", rows)
	}
}

func TestMapToTickers_DEXClassification(t *testing.T) {
	resp := &TickersResponse{Tickers: []TickerEntry{
		{Base: "mvrk", Target: "usdt", Market: TickerMarket{Identifier: "uniswap_v3", Name: "Uniswap V3"}, Last: ptrDec("1")},
	}}
	ex, _ := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(ex) != 1 || ex[0].Kind != tickers.ExchangeKindDEX {
		t.Errorf("dex classification failed: %+v", ex)
	}
}

func TestMapToTickers_NameFallbackToIdentifier(t *testing.T) {
	resp := &TickersResponse{Tickers: []TickerEntry{
		{Base: "mvrk", Target: "usdt", Market: TickerMarket{Identifier: "weird_dex", Name: ""}, Last: ptrDec("1")},
	}}
	ex, _ := MapToTickers(resp, prices.SourceCoinGecko, "mvrk", time.Now())
	if len(ex) != 1 || ex[0].Name != "weird_dex" {
		t.Errorf("name fallback: %+v", ex)
	}
}

// TestTickerEntry_JSONDecode pins the JSON shape against CG's actual response.
func TestTickerEntry_JSONDecode(t *testing.T) {
	body := `{
		"tickers": [{
			"base":"MVRK","target":"BTC",
			"market":{"name":"Binance","identifier":"binance","has_trading_incentive":false,"logo":"x.png"},
			"last": "0.000021",
			"volume": "1234567.89",
			"converted_last": {"usd": "0.085"},
			"converted_volume": {"usd": "22000"},
			"trust_score":"green",
			"bid_ask_spread_percentage":"0.1",
			"timestamp":"2026-05-29T11:00:00Z",
			"last_traded_at":"2026-05-29T11:00:00Z",
			"last_fetch_at":"2026-05-29T11:05:00Z",
			"is_anomaly": false,
			"is_stale": false,
			"trade_url":"https://binance.com/trade/MVRK_BTC"
		}]
	}`
	var resp TickersResponse
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tickers) != 1 {
		t.Fatalf("tickers = %d", len(resp.Tickers))
	}
	e := resp.Tickers[0]
	if e.Market.Identifier != "binance" || e.Market.Logo != "x.png" {
		t.Errorf("market: %+v", e.Market)
	}
	if e.Last == nil || !e.Last.Equal(dec("0.000021")) {
		t.Errorf("last = %v", e.Last)
	}
	if v, ok := e.ConvertedLast["usd"]; !ok || !v.Equal(dec("0.085")) {
		t.Errorf("converted_last[usd] = %v ok=%v", v, ok)
	}
	if e.BidAskSpreadPercentage == nil || !e.BidAskSpreadPercentage.Equal(dec("0.1")) {
		t.Errorf("bid_ask_spread = %v", e.BidAskSpreadPercentage)
	}
}
