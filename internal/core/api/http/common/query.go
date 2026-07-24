package common

import (
	"strconv"
	"strings"
	"time"

	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

// PriceQuery is the parsed shape of a list-query (range or latest), shared by the
// FT and RWA endpoints.
type PriceQuery struct {
	From    time.Time
	To      time.Time
	Limit   int
	Metrics []string // optional metric filter (currencies for FT, sides for RWA)
}

// QueryOptions configures BindPriceQuery.
type QueryOptions struct {
	// DefaultLatestLimit is used when no time window AND no explicit ?limit are present.
	DefaultLatestLimit int
	// MaxLimit caps ?limit; rejected with 400 when exceeded. 0 disables the cap (not recommended).
	MaxLimit int
	// MetricParam is the query-string key for the metric filter, e.g. "currency" or "side".
	// Empty disables metric filtering on this endpoint.
	MetricParam string
}

// BindPriceQuery parses from, to (RFC3339), limit, and metric query params.
//
// Semantics:
//   - both from and to absent → latest mode (window stays zero/zero, Limit defaults to DefaultLatestLimit)
//   - either from or to set   → window mode; the missing side defaults to "now-24h" / "now"
//   - limit must be > 0; capped by MaxLimit (returns 400 on overflow)
//   - metric is comma-separated; empty entries are dropped
func BindPriceQuery(c *gin.Context, opts QueryOptions) (PriceQuery, error) {
	now := time.Now()
	fromStr := strings.TrimSpace(c.Query("from"))
	toStr := strings.TrimSpace(c.Query("to"))
	limitStr := strings.TrimSpace(c.Query("limit"))

	var q PriceQuery
	useWindow := false

	if fromStr != "" {
		t, err := parseRFC3339(fromStr, "from")
		if err != nil {
			return PriceQuery{}, err
		}
		q.From = t
		useWindow = true
	}
	if toStr != "" {
		t, err := parseRFC3339(toStr, "to")
		if err != nil {
			return PriceQuery{}, err
		}
		q.To = t
		useWindow = true
	}
	if useWindow {
		if fromStr == "" {
			q.From = now.Add(-24 * time.Hour)
		}
		if toStr == "" {
			q.To = now
		}
		if q.From.After(q.To) {
			return PriceQuery{}, coreerrors.InvalidArgument("Invalid time range: 'from' must be before 'to'")
		}
	}

	if limitStr != "" {
		lim, err := strconv.Atoi(limitStr)
		if err != nil || lim <= 0 {
			return PriceQuery{}, coreerrors.InvalidArgument("Invalid 'limit' parameter: must be a positive integer")
		}
		if opts.MaxLimit > 0 && lim > opts.MaxLimit {
			return PriceQuery{}, coreerrors.InvalidArgument("Invalid 'limit' parameter: exceeds maximum")
		}
		q.Limit = lim
	}
	if !useWindow && q.Limit == 0 && opts.DefaultLatestLimit > 0 {
		q.Limit = opts.DefaultLatestLimit
	}
	// Window mode with no explicit ?limit: apply the hard server cap so a caller
	// cannot force an unbounded full-range scan (e.g. from=1970 → millions of
	// minute-cadence rows materialized in memory and JSON-serialized). The
	// repositories add a SQL LIMIT only when q.Limit > 0, so leaving it at 0 here
	// means "no LIMIT" — an unauthenticated DoS on the public list endpoints.
	if useWindow && q.Limit == 0 && opts.MaxLimit > 0 {
		q.Limit = opts.MaxLimit
	}

	if opts.MetricParam != "" {
		if raw := strings.TrimSpace(c.Query(opts.MetricParam)); raw != "" {
			parts := strings.Split(raw, ",")
			for _, p := range parts {
				p = strings.TrimSpace(strings.ToLower(p))
				if p != "" {
					q.Metrics = append(q.Metrics, p)
				}
			}
		}
	}
	return q, nil
}

func parseRFC3339(value, paramName string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, coreerrors.InvalidArgument(
			"Invalid '" + paramName + "' parameter: use RFC3339 (e.g. 2025-01-01T00:00:00Z)",
		)
	}
	return t, nil
}
