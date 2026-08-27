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
	// minStorableExponent bounds the SMALL side. Anything below the column's
	// scale stores as zero anyway, but the encoder does not know that: pgx
	// renders numeric digit-by-digit, so a value like 1e-1000000000 — which
	// decimal.NewFromString accepts, any int32 exponent being legal — costs
	// quadratic time and gigabytes before Postgres ever sees the statement.
	minStorableExponent = -(storableScale + maxStorableDigits)
)

// mappablePrice normalises one upstream price for storage: skip non-positive
// values, drop the ones that cannot be stored, and apply the quote-decimals
// shift. reason is empty for a routine skip (zero/negative) and set for a drop
// the caller must count.
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

// unstorable decides on metadata first. Comparing against a bound with Cmp
// would rescale the operands, materialising a 10^n big.Int — and n comes
// straight from an untrusted exponent, so "1e1000000000" (or its negative
// twin) would hang the tick.
func unstorable(d decimal.Decimal) bool {
	if d.Exponent() < minStorableExponent {
		return true
	}
	if coefficientDigits(d)+int(d.Exponent()) > maxStorableDigits {
		return true
	}
	// Both magnitudes are now bounded, so rescaling is cheap. Postgres rounds
	// to the column scale: that rounding can carry into a 21st integer digit
	// (99999999999999999999.9999…95), and a positive value below the scale
	// would land as 0 — token_prices doubles as the FX source, so a stored
	// zero rate is worse than a missing row.
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
