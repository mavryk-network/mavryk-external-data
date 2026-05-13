package prices

import (
	"strings"
	"time"
)

// Period is a price-change lookback window — the wire/storage representation
// for the /v1/prices/:token/change and /v1/rwa/:symbol/change endpoints.
// Mirrors the apiprices.Interval pattern so the parsing rules and CA-mapping
// conventions stay consistent across chart and change endpoints.
type Period string

const (
	Period1h  Period = "1h"
	Period24h Period = "24h"
	Period7d  Period = "7d"
	Period30d Period = "30d"
)

// AllPeriods is the canonical accept-list, ordered shortest→longest.
// Order matters for fixture-based tests and default response shape;
// do not sort alphabetically.
var AllPeriods = []Period{Period1h, Period24h, Period7d, Period30d}

// DefaultChangePeriods is the response default when ?periods= is omitted.
// Matches the contract documented in the design doc / openapi.yaml.
var DefaultChangePeriods = []Period{Period24h, Period7d, Period30d}

// ParsePeriod normalises s (lower-case, trim) and validates it against
// AllPeriods. Empty string returns ok=false — callers decide whether
// "missing" maps to a default or to a 400.
func ParsePeriod(s string) (Period, bool) {
	p := Period(strings.ToLower(strings.TrimSpace(s)))
	if p == "" {
		return "", false
	}
	for _, v := range AllPeriods {
		if v == p {
			return v, true
		}
	}
	return "", false
}

// Duration returns the wall-clock span of the period. Always ok for
// AllPeriods entries; ok=false for the zero value or unknown.
//
// Note: uses Add(-N*24h) semantics — wall-clock UTC, not calendar days.
// The service is UTC-only so DST never enters the picture.
func (p Period) Duration() (time.Duration, bool) {
	switch p {
	case Period1h:
		return time.Hour, true
	case Period24h:
		return 24 * time.Hour, true
	case Period7d:
		return 7 * 24 * time.Hour, true
	case Period30d:
		return 30 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// StalenessBudget returns the maximum acceptable age (relative to `now-period`)
// of the at-or-before anchor sample. The repository scans raw `token_prices` /
// `rwa_quote_prices` (not CAs) and picks the latest row whose `ts` is
// in `[now-period-staleness, now-period]`. If no row falls in that window
// the period returns a `Found=false` anchor and the wire renders nulls
// (Decision #3 / #18).
//
// Why per-period staleness:
//   - 1h period over 1m-cadence data: 6 min covers a brief outage without
//     letting a stale 30-min-old anchor pretend to be "1h ago".
//   - 24h period: 1h slack absorbs ingestion lag and CoinGecko hiccups but
//     keeps the anchor inside the same trading session.
//   - 7d / 30d: 12h slack is generous because daily candles are noisy on
//     thin pairs; one missed day shouldn't blank the period.
//
// Returns ok=false for unknown periods.
func (p Period) StalenessBudget() (time.Duration, bool) {
	switch p {
	case Period1h:
		return 6 * time.Minute, true
	case Period24h:
		return 1 * time.Hour, true
	case Period7d:
		return 12 * time.Hour, true
	case Period30d:
		return 12 * time.Hour, true
	default:
		return 0, false
	}
}

// AnchorWindow returns the [lo, hi] timestamp bracket for the at-or-before
// anchor sample, where:
//
//	hi = now − period
//	lo = now − period − staleness
//
// Repositories scan raw price tables filtered by `ts >= lo AND ts <= hi`
// and ORDER BY ts DESC LIMIT 1 to get the latest sample no later than
// `now-period`. The lower bound prevents the query from pulling a
// year-old sample after a long ingestion gap — an absent anchor maps
// to JSON null per Decision #3.
//
// Returns ok=false for unknown periods.
func (p Period) AnchorWindow(now time.Time) (lo, hi time.Time, ok bool) {
	dur, dok := p.Duration()
	stale, sok := p.StalenessBudget()
	if !dok || !sok {
		return time.Time{}, time.Time{}, false
	}
	hi = now.Add(-dur)
	lo = hi.Add(-stale)
	return lo, hi, true
}

// String implements fmt.Stringer and is the canonical wire form.
func (p Period) String() string { return string(p) }
