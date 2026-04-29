// Package prices defines the domain types for the long-format price model.
//
// Conceptually every measurement in the system is:
//
//	at time `Timestamp`, source `Source` reported that for entity `EntityKey`
//	the metric `Metric` had value `Price` (with optional Size).
//
// FT (token_prices) and RWA (rwa_quote_prices) reuse the same shape — they only
// differ in what the EntityKey/Metric mean (token+currency vs pair_id+side).
package prices

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// PricePoint is one measurement. Domain-level; storage entities map onto this.
type PricePoint struct {
	Source    Source
	EntityKey string // FT: token symbol; RWA: pair_id rendered as decimal string
	Timestamp time.Time
	Metric    string // FT: Currency.String(); RWA: Side.String()
	Price     decimal.Decimal
	// Size is non-nil only for orderbook-source rows (RWA). FT-side leaves it nil.
	Size *decimal.Decimal
}

// Snapshot is the transposed form: all metrics for one entity at one timestamp.
// The HTTP layer renders this as JSON for "latest" endpoints.
type Snapshot struct {
	Source    Source                     `json:"source"`
	EntityKey string                     `json:"entity"`
	Timestamp time.Time                  `json:"timestamp"`
	Values    map[string]decimal.Decimal `json:"values"`
}

// LatestSnapshot collapses a flat slice into a Snapshot, taking the freshest
// observation per metric. Returns ok=false on empty input.
func LatestSnapshot(points []PricePoint) (Snapshot, bool) {
	if len(points) == 0 {
		return Snapshot{}, false
	}
	// Index by metric, keep the freshest.
	freshest := make(map[string]PricePoint, len(points))
	for _, p := range points {
		if cur, ok := freshest[p.Metric]; !ok || p.Timestamp.After(cur.Timestamp) {
			freshest[p.Metric] = p
		}
	}
	// Pick the overall newest timestamp as the snapshot timestamp.
	var newest time.Time
	values := make(map[string]decimal.Decimal, len(freshest))
	for metric, p := range freshest {
		values[metric] = p.Price
		if p.Timestamp.After(newest) {
			newest = p.Timestamp
		}
	}
	first := points[0]
	return Snapshot{
		Source:    first.Source,
		EntityKey: first.EntityKey,
		Timestamp: newest,
		Values:    values,
	}, true
}

// SortByTimestampAsc sorts in place. Stable on (ts, metric) ordering.
func SortByTimestampAsc(points []PricePoint) {
	sort.SliceStable(points, func(i, j int) bool {
		if !points[i].Timestamp.Equal(points[j].Timestamp) {
			return points[i].Timestamp.Before(points[j].Timestamp)
		}
		return points[i].Metric < points[j].Metric
	})
}
