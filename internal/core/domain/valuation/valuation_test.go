package valuation

import (
	"encoding/json"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestCalculateExactSupplyAndIndependentQuality(t *testing.T) {
	at := time.Date(2026, 11, 3, 10, 57, 0, 0, time.UTC)
	scale := 6
	price := decimal.NewFromInt(100)
	policy := Policy{}.Defaulted()
	q := policy.Quality(at, at, "")
	supply := Supply{Total: "50000000000", Circulating: "40000000000", Metadata: Metadata{Decimals: &scale}, TotalQuality: q, CirculatingQuality: q}
	point := Point{Price: &price, PriceAt: at, AsOf: at, PriceBasis: "last"}
	result := Calculate(supply, point, policy)
	require.Equal(t, "4000000", result.MarketCap.String())
	require.Equal(t, "5000000", result.FDV.String())
	supply.Circulating = "40100000000"
	require.Equal(t, "4010000", Calculate(supply, point, policy).MarketCap.String())
	supply.Circulating = "0"
	zero := Calculate(supply, point, policy)
	require.NotNil(t, zero.MarketCap)
	require.True(t, zero.MarketCap.IsZero())
	supply.Circulating = ""
	supply.CirculatingQuality = Missing("treasury_unknown")
	missing := Calculate(supply, point, policy)
	require.Nil(t, missing.MarketCap)
	require.Equal(t, "5000000", missing.FDV.String())
	require.Equal(t, "treasury_unknown", missing.MarketCapQuality.Reason)
	supply.Metadata.Reason = "decimals_conflict"
	conflict := Calculate(supply, point, policy)
	require.Nil(t, conflict.FDV)
	require.Equal(t, "fresh", conflict.PriceQuality.Status)
	for _, v := range []string{"-1", "01", "1.1", "unknown"} {
		_, err := Money(v, scale, price)
		require.Error(t, err)
	}
	huge, err := Money("9007199254740993000000", 6, decimal.RequireFromString("0.125"))
	require.NoError(t, err)
	require.Equal(t, "1125899906842624.125", huge.String())
}

func TestLatestAgingPreservesSourceDeadlinesAndHistoricalValues(t *testing.T) {
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	scale := 0
	price := decimal.NewFromInt(10)
	policy := Policy{}.Defaulted()
	q := policy.Quality(at, at, "")
	supply := Supply{Total: "10", Circulating: "5", Metadata: Metadata{Decimals: &scale}, TotalQuality: q, CirculatingQuality: q}
	p := Calculate(supply, Point{Price: &price, PriceAt: at, AsOf: at, PriceBasis: "last"}, policy)
	p.Conversions = map[string]Conversion{"eur": {MarketCapQuality: Missing("fx_unavailable"), FDVQuality: Missing("fx_unavailable")}, "usd": {MarketCap: p.MarketCap, FDV: p.FDV, MarketCapQuality: q, FDVQuality: q}}
	s := Snapshot{Revision: "9007199254740993", Supply: supply, Valuations: []Point{p}}
	stale := s.At(at.Add(time.Hour))
	require.Equal(t, "stale", stale.Valuations[0].MarketCapQuality.Status)
	require.Equal(t, q.FreshUntil, stale.Valuations[0].MarketCapQuality.FreshUntil)
	require.Equal(t, "unavailable", stale.Valuations[0].Conversions["eur"].MarketCapQuality.Status)
	expired := s.At(at.Add(25 * time.Hour))
	require.Nil(t, expired.Valuations[0].MarketCap)
	require.Nil(t, expired.Valuations[0].Conversions["usd"].MarketCap)
	require.NotNil(t, s.Valuations[0].MarketCap)
	require.Equal(t, "fresh", s.Valuations[0].MarketCapQuality.Status)
	b, err := json.Marshal(stale)
	require.NoError(t, err)
	require.Contains(t, string(b), `"revision":"9007199254740993"`)
	require.Contains(t, string(b), `"market_cap":"50"`)
	require.Equal(t, "unavailable", policy.Quality(at.Add(time.Second), at, "last").Status)
}

func TestPricePolicyAgeNoFallback(t *testing.T) {
	at := time.Now().UTC()
	scale := 0
	price := decimal.NewFromInt(10)
	policy := Policy{}.Defaulted()
	q := policy.Quality(at, at, "")
	s := Supply{Total: "10", Circulating: "5", Metadata: Metadata{Decimals: &scale}, TotalQuality: q, CirculatingQuality: q}
	for _, tc := range []struct {
		basis  string
		age    time.Duration
		status string
	}{{"last", time.Minute, "fresh"}, {"last", time.Hour, "stale"}, {"primary_list_price", time.Hour, "fresh"}, {"primary_list_price", 2 * time.Hour, "stale"}, {"last", 25 * time.Hour, "unavailable"}} {
		p := Calculate(s, Point{Price: &price, PriceAt: at.Add(-tc.age), AsOf: at, PriceBasis: tc.basis}, policy)
		require.Equal(t, tc.status, p.MarketCapQuality.Status)
	}
	absent := Calculate(s, Point{AsOf: at, PriceBasis: "last"}, policy)
	require.Nil(t, absent.MarketCap)
	require.Equal(t, "price_missing", absent.MarketCapQuality.Reason)
}
