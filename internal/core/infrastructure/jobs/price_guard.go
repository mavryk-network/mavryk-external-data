package jobs

import (
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"github.com/shopspring/decimal"
)

// Drop reasons for metrics.IngestRowsDroppedTotal. Closed set.
const (
	dropReasonNonFinite  = "non_finite"
	dropReasonOutOfRange = "out_of_range"
)

// numeric(38,18) holds 20 integer digits and 18 fractional ones. A value
// outside that aborts the whole INSERT batch, taking every good row with it.
const (
	maxStorableDigits = 20
	storableScale     = 18
	// Bounds the SMALL side: pgx renders numeric digit-by-digit, so an accepted
	// exponent like 1e-1000000000 costs gigabytes before Postgres sees it.
	minStorableExponent = -(storableScale + maxStorableDigits)
)

// mappablePrice normalises one upstream price for storage. reason is empty for
// a routine skip (zero/negative) and set for a drop the caller must count.
func mappablePrice(v equiteez.FlexibleFloat, shift int32) (price decimal.Decimal, reason string, ok bool) {
	if v.NonFinite() {
		return decimal.Decimal{}, dropReasonNonFinite, false
	}
	d := v.Decimal()
	if !d.IsPositive() {
		return decimal.Decimal{}, "", false
	}
	if shift != 0 {
		d = d.Shift(shift)
	}
	if unstorable(d) {
		return decimal.Decimal{}, dropReasonOutOfRange, false
	}
	return d, "", true
}

// unstorable decides on metadata first: comparing with Cmp would rescale and
// materialise a 10^n big.Int from an untrusted exponent, hanging the tick.
func unstorable(d decimal.Decimal) bool {
	if d.Exponent() < minStorableExponent {
		return true
	}
	if coefficientDigits(d)+int(d.Exponent()) > maxStorableDigits {
		return true
	}
	// Bounded now, so rescaling is cheap. Postgres rounds to the column scale:
	// that can carry into a 21st integer digit, and a value below the scale
	// lands as 0 — a stored zero FX rate is worse than a missing row.
	r := d.Round(storableScale)
	if r.IsZero() {
		return true
	}
	return coefficientDigits(r)+int(r.Exponent()) > maxStorableDigits
}

func coefficientDigits(d decimal.Decimal) int {
	s := d.Coefficient().String()
	if len(s) > 0 && s[0] == '-' {
		return len(s) - 1
	}
	return len(s)
}
