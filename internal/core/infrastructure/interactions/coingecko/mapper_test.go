package coingecko

import (
	"testing"

	"quotes/internal/core/domain/prices"
)

func TestMapToPricePoints_BasicMapping(t *testing.T) {
	resp := &MarketChartRangeResponse{
		Prices: [][]float64{
			{1700000000000, 1.5}, // ms epoch
			{1700000060000, 1.6},
		},
	}
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.CurrencyUSD: resp,
	}
	points := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(points) != 2 {
		t.Fatalf("len(points) = %d, want 2", len(points))
	}
	for _, p := range points {
		if p.Source != prices.SourceCoinGecko {
			t.Errorf("source = %q, want coingecko", p.Source)
		}
		if p.EntityKey != "mvrk" {
			t.Errorf("entity = %q, want mvrk", p.EntityKey)
		}
		if p.Metric != "usd" {
			t.Errorf("metric = %q, want usd", p.Metric)
		}
	}
}

func TestMapToPricePoints_MultipleCurrenciesAreSorted(t *testing.T) {
	usd := &MarketChartRangeResponse{Prices: [][]float64{{1700000000000, 1.5}}}
	eur := &MarketChartRangeResponse{Prices: [][]float64{{1700000000000, 1.4}}}
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.CurrencyUSD: usd,
		prices.CurrencyEUR: eur,
	}
	points := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(points) != 2 {
		t.Fatalf("len = %d, want 2", len(points))
	}
	// Same timestamp → sort by metric ascending: eur < usd
	if points[0].Metric != "eur" || points[1].Metric != "usd" {
		t.Errorf("order = %q,%q; want eur,usd", points[0].Metric, points[1].Metric)
	}
}

func TestMapToPricePoints_EmptyInput(t *testing.T) {
	if got := MapToPricePoints(prices.SourceCoinGecko, "mvrk", nil); got != nil {
		t.Errorf("nil → %+v, want nil", got)
	}
	if got := MapToPricePoints(prices.SourceCoinGecko, "mvrk", map[prices.Currency]*MarketChartRangeResponse{}); got != nil {
		t.Errorf("empty map → %+v, want nil", got)
	}
}

func TestMapToPricePoints_SkipsMalformedRows(t *testing.T) {
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.CurrencyUSD: {
			Prices: [][]float64{
				{1700000000000, 1.5},
				{1700000060000}, // malformed (1 element)
			},
		},
	}
	points := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(points) != 1 {
		t.Errorf("len = %d, want 1 (malformed dropped)", len(points))
	}
}

func TestMapToPricePoints_DedupsDuplicateTimestamps(t *testing.T) {
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.CurrencyUSD: {
			Prices: [][]float64{
				{1700000000000, 1.5},
				{1700000000000, 1.7}, // duplicate ts — must keep the last sample
				{1700000060000, 1.6},
			},
		},
		prices.CurrencyEUR: {
			Prices: [][]float64{
				{1700000000000, 1.4}, // same ts, different currency — kept
			},
		},
	}
	points := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(points) != 3 {
		t.Fatalf("len = %d, want 3 (duplicate (currency,ts) collapsed)", len(points))
	}
	for _, p := range points {
		if p.Metric == "usd" && p.Timestamp.UnixMilli() == 1700000000000 {
			if p.Price.String() != "1.7" {
				t.Errorf("dedup kept price %s, want the last sample 1.7", p.Price)
			}
		}
	}
}

// A >=1e20 value overflows numeric(38,18) and would abort the whole INSERT
// batch — it must be dropped, not mapped.
func TestMapToPricePoints_DropsUnstorableMagnitude(t *testing.T) {
	data := map[prices.Currency]*MarketChartRangeResponse{
		prices.CurrencyUSD: {
			Prices: [][]float64{
				{1700000000000, 1e20},   // at the bound — dropped
				{1700000060000, 9.9e19}, // just under — kept
			},
		},
	}
	points := MapToPricePoints(prices.SourceCoinGecko, "mvrk", data)
	if len(points) != 1 {
		t.Fatalf("len = %d, want 1 (>=1e20 dropped, 9.9e19 kept)", len(points))
	}
	if points[0].Timestamp.UnixMilli() != 1700000060000 {
		t.Fatalf("kept the wrong sample: ts=%d", points[0].Timestamp.UnixMilli())
	}
}
