package coingecko

import (
	"sort"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

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
			ts := time.UnixMilli(int64(point[0])).UTC()
			price := decimal.NewFromFloat(point[1])
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

	return out
}
