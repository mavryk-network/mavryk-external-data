package prices

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"quotes/internal/core/domain/prices"
)

type primaryHistoryFake struct {
	terms PrimaryTerms
	err   error
}

func (s primaryHistoryFake) Terms(context.Context, PrimaryRequest) (PrimaryTerms, error) {
	return s.terms, s.err
}

func TestPrimaryValuator_UsesAtTimeUSDTAndRefusesStaleFX(t *testing.T) {
	registerFXTokens(t)
	base := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		at            time.Time
		price, reason string
	}{
		{"early", base.Add(10 * time.Second), "725", ""},
		{"late", base.Add(50 * time.Second), "652.5", ""},
		{"stale", base.Add(10 * time.Minute), "", "fx_stale"},
		{"missing", base.Add(-time.Hour), "", "fx_missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := NewTokenFXConverter(&fakeFXSource{points: []prices.PricePoint{
				{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: base, Price: decimal.RequireFromString("1")},
				{Source: prices.SourceCoinGecko, EntityKey: "usdt", Metric: "usd", Timestamp: base.Add(30 * time.Second), Price: decimal.RequireFromString("0.9")},
			}}, time.Minute, prices.SourceCoinGecko)
			service := &PrimaryValuator{History: primaryHistoryFake{terms: PrimaryTerms{Price: "725"}}, Converter: fx}
			got := service.Evaluate(context.Background(), []PrimaryRequest{{At: tc.at}})[0]
			if got.Reason != tc.reason {
				t.Fatalf("reason=%s want=%s", got.Reason, tc.reason)
			}
			if tc.price != "" && (got.USD == nil || *got.USD != tc.price) {
				t.Fatalf("USD=%v want=%s", got.USD, tc.price)
			}
			if tc.price == "" && got.USD != nil {
				t.Fatal("unknown FX became confirmed USD")
			}
		})
	}
}

func TestPrimaryValuator_UnknownHistoryKeepsReason(t *testing.T) {
	service := &PrimaryValuator{History: primaryHistoryFake{err: PrimaryMissing("same_block_change")}}
	got := service.Evaluate(context.Background(), []PrimaryRequest{{Level: 42}})[0]
	if got.USD != nil || got.Reason != "same_block_change" || got.Level != 42 {
		t.Fatalf("got %+v", got)
	}
}
