package coingecko

import (
	"testing"

	"quotes/internal/core/domain/prices"
)

// TestMapToPricePoints_SkipsNonPositive pins that zero/negative/non-finite price
// samples are dropped rather than persisted. A persisted 0.0 would overwrite a
// good price at the same (token,currency,ts) and make every ?in= FX conversion in
// that minute bucket resolve to a rate of 0.
func TestMapToPricePoints_SkipsNonPositive(t *testing.T) {
	// Timestamps in epoch-ms after 2010 (below-floor ts are dropped separately).
	const t0 = 1_700_000_000_000 // ~2023-11-14
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.Currency("usd"): {
			Prices: [][]float64{
				{t0, 0},           // zero — dropped
				{t0 + 60_000, -5}, // negative — dropped
				{t0 + 120_000, 1.5},
				{t0 + 180_000, 2.25},
			},
		},
	}
	out := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(out) != 2 {
		t.Fatalf("got %d points, want 2 (zero + negative dropped)", len(out))
	}
	for _, p := range out {
		if !p.Price.IsPositive() {
			t.Errorf("non-positive price survived: %s", p.Price)
		}
	}
}

// TestMapToPricePoints_DropsOutOfRangeTimestamp verifies the ts bounds guard: a
// below-2010 or absurd-future ms value is skipped rather than converted to a
// garbage int64 row.
func TestMapToPricePoints_DropsOutOfRangeTimestamp(t *testing.T) {
	const valid = 1_700_000_000_000
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.Currency("usd"): {
			Prices: [][]float64{
				{1_000, 1.0},  // pre-2010 — dropped
				{1e300, 1.0},  // absurd future / overflow — dropped
				{valid, 3.14}, // kept
			},
		},
	}
	out := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(out) != 1 {
		t.Fatalf("got %d points, want 1 (out-of-range timestamps dropped)", len(out))
	}
}
