package jobs

import (
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"github.com/shopspring/decimal"
)

func TestOrderbookToPoints_NormalizesByQuoteDecimals(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	pair := prices.RWAPair{
		ID:          42,
		Source:      prices.SourceEquiteez,
		BaseSymbol:  "TST",
		QuoteSymbol: "USDT",
		Enabled:     true,
	}

	cases := []struct {
		name          string
		raw           float64
		quoteDecimals int
		wantPrice     string
	}{
		// USDT has 6 decimals → micro-USDT raw shifts by -6.
		{"micro_usdt_56250000", 56_250_000, 6, "56.25"},
		{"micro_usdt_1000000", 1_000_000, 6, "1"},
		// 0 decimals = no normalization (legacy / unknown-currency case).
		{"no_normalization", 100, 0, "100"},
		// 8-decimal asset (e.g. BTC-style quote) — defensive.
		{"satoshi_like", 250_000_000, 8, "2.5"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ob := &equiteez.EquiteezOrderbook{
				HighestBuyPrice:  equiteez.FlexibleFloatFromDecimal(decimal.NewFromFloat(c.raw)),
				LowestSellPrice:  equiteez.FlexibleFloatFromDecimal(decimal.NewFromFloat(c.raw)),
				LastMatchedPrice: equiteez.FlexibleFloatFromDecimal(decimal.NewFromFloat(c.raw)),
			}
			pts := orderbookToPoints(pair, ob, c.quoteDecimals, now)
			if len(pts) != 3 {
				t.Fatalf("len(pts) = %d, want 3 (bid/ask/last all positive)", len(pts))
			}
			for _, p := range pts {
				if got := p.Price.String(); got != c.wantPrice {
					t.Errorf("metric=%s price = %q, want %q (raw=%v decimals=%d)",
						p.Metric, got, c.wantPrice, c.raw, c.quoteDecimals)
				}
			}
		})
	}
}

func TestOrderbookToPoints_SkipsZeroAndNegative(t *testing.T) {
	pair := prices.RWAPair{ID: 1, Source: prices.SourceEquiteez, QuoteSymbol: "USDT"}
	ob := &equiteez.EquiteezOrderbook{
		HighestBuyPrice:  equiteez.FlexibleFloatFromDecimal(decimal.Zero),           // skip
		LowestSellPrice:  equiteez.FlexibleFloatFromDecimal(decimal.NewFromInt(-5)), // skip
		LastMatchedPrice: equiteez.FlexibleFloatFromDecimal(decimal.NewFromInt(1_000_000)),
	}
	pts := orderbookToPoints(pair, ob, 6, time.Now())
	if len(pts) != 1 {
		t.Fatalf("len(pts) = %d, want 1 (only `last` is positive)", len(pts))
	}
	if pts[0].Metric != string(prices.SideLast) {
		t.Errorf("metric = %q, want %q", pts[0].Metric, prices.SideLast)
	}
}

func TestLookupQuoteDecimals(t *testing.T) {
	// Save & restore registry so tests are hermetic.
	prices.RegisterTokens([]prices.TokenInfo{
		{Symbol: "usdt", Name: "Tether", Decimals: 6, Enabled: true},
		{Symbol: "mvrk", Name: "Mavryk", Decimals: 6, Enabled: true},
	})

	if d, ok := lookupQuoteDecimals("USDT"); !ok || d != 6 {
		t.Errorf("usdt: got (%d, %v), want (6, true)", d, ok)
	}
	if d, ok := lookupQuoteDecimals("mvrk"); !ok || d != 6 {
		t.Errorf("mvrk lowercase: got (%d, %v), want (6, true)", d, ok)
	}
	if _, ok := lookupQuoteDecimals("xyz"); ok {
		t.Errorf("unknown symbol must return ok=false")
	}
	if _, ok := lookupQuoteDecimals(""); ok {
		t.Errorf("empty symbol must return ok=false")
	}
}
