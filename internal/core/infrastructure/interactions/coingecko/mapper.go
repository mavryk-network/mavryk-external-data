package coingecko

import (
	"math"
	"sort"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// minValidMillis is 2010-01-01T00:00:00Z in epoch milliseconds — the floor for
// an accepted CoinGecko sample timestamp (no crypto price data predates it).
const minValidMillis = 1262304000000

// maxStorablePrice is 10^20 — the integer-part capacity of numeric(38,18).
const maxStorablePrice = 1e20

// MapToPricePoints converts the CoinGecko market_chart/range response (one per
// currency) into long-format []PricePoint rows for token_prices. The forward-fill
// behaviour from the old wide-table mapper is gone — long-format records sparse
// data accurately. Forward-fill, if needed, becomes a query-time concern.
//
// The returned slice is sorted by (ts, currency) for stable test output and
// deterministic upsert order.
func MapToPricePoints(
	source prices.Source,
	tokenSymbol string,
	currencyData map[prices.Currency]*MarketChartRangeResponse,
) []prices.PricePoint {
	if len(currencyData) == 0 {
		return nil
	}

	var out []prices.PricePoint
	for cur, data := range currencyData {
		if data == nil {
			continue
		}
		for _, point := range data.Prices {
			if len(point) < 2 {
				continue
			}
			// A glitched 0.0 would overwrite a good price at the same key and,
			// since token_prices doubles as the FX source, zero every ?in=
			// conversion in that bucket. maxStorablePrice bounds the integer
			// part — one oversized value aborts the whole INSERT batch.
			v := point[1]
			if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) || v >= maxStorablePrice {
				continue
			}
			// Bounds-check before the float→int64 conversion: an out-of-range
			// float converts to implementation-defined garbage (min-int64 on
			// amd64), a pre-1970 row that passes NOT NULL.
			tsMillis := point[0]
			maxMillis := float64(time.Now().Add(24 * time.Hour).UnixMilli())
			if tsMillis < minValidMillis || tsMillis > maxMillis {
				continue
			}
			ts := time.UnixMilli(int64(tsMillis)).UTC()
			price := decimal.NewFromFloat(v)
			out = append(out, prices.PricePoint{
				Source:    source,
				EntityKey: tokenSymbol,
				Timestamp: ts,
				Metric:    string(cur),
				Price:     price,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].Metric < out[j].Metric
	})

	// CoinGecko occasionally emits duplicate timestamps. Two rows with the same
	// (currency, ts) inside one INSERT ... ON CONFLICT batch fail with SQLSTATE
	// 21000 and poison the backfill chunk, so keep the last sample per key
	// (mirrors the Equiteez backfill dedup).
	dedup := out[:0]
	for _, p := range out {
		if n := len(dedup); n > 0 && dedup[n-1].Metric == p.Metric && dedup[n-1].Timestamp.Equal(p.Timestamp) {
			dedup[n-1] = p
			continue
		}
		dedup = append(dedup, p)
	}
	return dedup
}
