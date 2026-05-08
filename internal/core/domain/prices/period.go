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

// BackingCA returns the continuous-aggregate suffix used to look up the
// anchor price for this period. The mapping picks a CA whose bucket width
// is meaningfully smaller than the period itself, so the anchor is
// representative of "Δ ago" within ≤ one bucket of error:
//
//	1h  → "_1m"   (1m buckets, ≤1m error on a 60m span)
//	24h → "_1h"   (1h buckets, ≤1h error on a 24h span)
//	7d  → "_1d"   (1d buckets, ≤24h error on a 7d span)
//	30d → "_1d"   (1d buckets, ≤24h error on a 30d span)
//
// The empty string for unknown values is intentional — repositories
// switch on it and return a typed error; we never silently default.
func (p Period) BackingCA() string {
	switch p {
	case Period1h:
		return "_1m"
	case Period24h:
		return "_1h"
	case Period7d, Period30d:
		return "_1d"
	default:
		return ""
	}
}

// ToleranceWindow returns the [low, high] bracket of bucket-start
// timestamps to scan for the closest-before anchor, relative to `now`.
// `now - high` is always ≤ now-Δ; `now - low` is the loosest acceptable
// staleness. The 1h slot includes a one-extra-bucket grace because the
// _1m CA has end_offset=1m and may not have materialised the now-60m
// bucket yet at the start of a fresh minute.
//
//	1h:  [now-66m,         now-60m]
//	24h: [now-25h,         now-24h]
//	7d:  [now-7d12h,       now-7d]
//	30d: [now-30d12h,      now-30d]
//
// Returns ok=false for unknown periods.
func (p Period) ToleranceWindow(now time.Time) (lo, hi time.Time, ok bool) {
	switch p {
	case Period1h:
		return now.Add(-66 * time.Minute), now.Add(-60 * time.Minute), true
	case Period24h:
		return now.Add(-25 * time.Hour), now.Add(-24 * time.Hour), true
	case Period7d:
		return now.Add(-7*24*time.Hour - 12*time.Hour), now.Add(-7 * 24 * time.Hour), true
	case Period30d:
		return now.Add(-30*24*time.Hour - 12*time.Hour), now.Add(-30 * 24 * time.Hour), true
	default:
		return time.Time{}, time.Time{}, false
	}
}

// String implements fmt.Stringer and is the canonical wire form.
func (p Period) String() string { return string(p) }
