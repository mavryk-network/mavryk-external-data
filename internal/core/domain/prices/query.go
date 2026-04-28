package prices

import "time"

// Query bundles the read parameters for a price-series fetch.
// From == To == zero means "latest" (no time filter); Limit caps row count.
type Query struct {
	Source    Source
	EntityKey string
	Metrics   []string  // empty = all metrics for this entity
	From      time.Time // zero = no lower bound
	To        time.Time // zero = no upper bound
	Limit     int       // 0 = no limit (capped at MaxLimit by HTTP)
}

// IsLatest reports whether this query asks for the freshest rows (no time window).
func (q Query) IsLatest() bool { return q.From.IsZero() && q.To.IsZero() }

// HasMetricFilter reports whether Metrics narrows the result.
func (q Query) HasMetricFilter() bool { return len(q.Metrics) > 0 }
