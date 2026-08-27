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

// maxStorableDigits bounds a price's integer part. token_prices.price and
// rwa_quote_prices.price are numeric(38,18): 20 integer digits. A wider value
// would abort the whole INSERT batch, taking every good row with it.
const maxStorableDigits = 20

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
	if tooLargeToStore(d) {
		return decimal.Decimal{}, dropReasonOutOfRange, false
	}
	return d, "", true
}

// tooLargeToStore decides on metadata only. Comparing against a bound with
// Cmp would rescale the operands, materialising a 10^n big.Int — and n comes
// straight from an untrusted exponent, so "1e1000000000" would hang the tick.
func tooLargeToStore(d decimal.Decimal) bool {
	s := d.Coefficient().String()
	digits := len(s)
	if digits > 0 && s[0] == '-' {
		digits--
	}
	return digits+int(d.Exponent()) > maxStorableDigits
}
