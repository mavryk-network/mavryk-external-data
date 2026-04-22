package common

import (
	"strconv"
	"strings"
	"time"

	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

// QuotesQueryMode selects how optional from/to/limit query params are interpreted.
type QuotesQueryMode int

const (
	// QuotesQueryModeGetAll is for GET /quotes: always a time window; missing from/to default to last 24h and now.
	QuotesQueryModeGetAll QuotesQueryMode = iota
	// QuotesQueryModeByToken is for GET /:token: if neither from nor to is set, From/To stay zero (latest list);
	// if either is set, the other defaults to now-24h / now; when no time range and no limit query param, Limit defaults to DefaultLatestLimit.
	QuotesQueryModeByToken
)

// QuotesQueryOptions configures BindQuotesQuery.
type QuotesQueryOptions struct {
	Mode               QuotesQueryMode
	DefaultLatestLimit int // only QuotesQueryModeByToken; e.g. 100 when from/to omitted
}

// QuotesQuery holds parsed from/to/limit for quote list handlers (matches repository semantics:
// From and To both zero means "latest quotes" with Limit as max rows).
type QuotesQuery struct {
	From  time.Time
	To    time.Time
	Limit int
}

// TimeRange is an alias for QuotesQuery (see refactoring doc 2.4).
type TimeRange = QuotesQuery

// BindQuotesQuery parses from, to (RFC3339) and limit from the request query string.
func BindQuotesQuery(c *gin.Context, opts QuotesQueryOptions) (QuotesQuery, error) {
	fromStr := strings.TrimSpace(c.Query("from"))
	toStr := strings.TrimSpace(c.Query("to"))
	limitStr := strings.TrimSpace(c.Query("limit"))
	now := time.Now()

	switch opts.Mode {
	case QuotesQueryModeGetAll:
		return bindGetAllQuotesQuery(now, fromStr, toStr, limitStr)
	case QuotesQueryModeByToken:
		def := opts.DefaultLatestLimit
		if def <= 0 {
			def = 100
		}
		return bindByTokenQuotesQuery(now, fromStr, toStr, limitStr, def)
	default:
		return QuotesQuery{}, coreerrors.Internal("invalid quotes query bind mode", nil)
	}
}

func bindGetAllQuotesQuery(now time.Time, fromStr, toStr, limitStr string) (QuotesQuery, error) {
	q := QuotesQuery{
		From: now.Add(-24 * time.Hour),
		To:   now,
	}
	if fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return QuotesQuery{}, coreerrors.InvalidArgument("Invalid 'from' parameter: use RFC3339 (e.g. 2023-01-01T00:00:00Z)")
		}
		q.From = t
	}
	if toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return QuotesQuery{}, coreerrors.InvalidArgument("Invalid 'to' parameter: use RFC3339 (e.g. 2023-01-01T00:00:00Z)")
		}
		q.To = t
	}
	if limitStr != "" {
		lim, err := parsePositiveLimitQuery(limitStr)
		if err != nil {
			return QuotesQuery{}, err
		}
		q.Limit = lim
	}
	if q.From.After(q.To) {
		return QuotesQuery{}, coreerrors.InvalidArgument("Invalid time range: 'from' must be before 'to'")
	}
	return q, nil
}

func bindByTokenQuotesQuery(now time.Time, fromStr, toStr, limitStr string, defaultLatestLimit int) (QuotesQuery, error) {
	var q QuotesQuery
	useTimeRange := false

	if fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return QuotesQuery{}, coreerrors.InvalidArgument("Invalid 'from' parameter: use RFC3339 (e.g. 2023-01-01T00:00:00Z)")
		}
		q.From = t
		useTimeRange = true
	}
	if toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			return QuotesQuery{}, coreerrors.InvalidArgument("Invalid 'to' parameter: use RFC3339 (e.g. 2023-01-01T00:00:00Z)")
		}
		q.To = t
		useTimeRange = true
	}
	if useTimeRange {
		if fromStr == "" {
			q.From = now.Add(-24 * time.Hour)
		}
		if toStr == "" {
			q.To = now
		}
		if q.From.After(q.To) {
			return QuotesQuery{}, coreerrors.InvalidArgument("Invalid time range: 'from' must be before 'to'")
		}
	}

	if limitStr != "" {
		lim, err := parsePositiveLimitQuery(limitStr)
		if err != nil {
			return QuotesQuery{}, err
		}
		q.Limit = lim
	}
	if !useTimeRange && q.Limit == 0 {
		q.Limit = defaultLatestLimit
	}
	return q, nil
}

func parsePositiveLimitQuery(limitStr string) (int, error) {
	lim, err := strconv.Atoi(limitStr)
	if err != nil || lim <= 0 {
		return 0, coreerrors.InvalidArgument("Invalid 'limit' parameter: must be a positive integer")
	}
	return lim, nil
}
