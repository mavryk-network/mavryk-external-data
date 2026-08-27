package handlers

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/shopspring/decimal"
)

// TestRoundForWire_Table pins both branches, including the values from the
// review that used to render as 0 or with a 46% error.
func TestRoundForWire_Table(t *testing.T) {
	cases := []struct{ in, want string }{
		// At or above the threshold: identical to the historical Round(6).
		{"56.25", "56.25"},
		{"100.45", "100.45"},
		{"0.07154123456789", "0.071541"},
		{"0.06094123456789", "0.060941"},
		{"112.987654321", "112.987654"},
		{"1234.5678", "1234.5678"},
		{"0.01", "0.01"},
		{"0.0100685", "0.010069"},
		// Pins the threshold from BELOW: lowering it would take these into the
		// significant-digit branch and change the bytes.
		{"0.00999999999", "0.01"},
		{"0.0012345678", "0.00123457"},
		{"-4.3010293112", "-4.301029"},
		{"0", "0"},
		// Below it: 6 significant digits instead of a destroyed value.
		{"0.00000068464", "0.00000068464"},
		{"0.0000241235", "0.0000241235"},
		{"0.0000100421", "0.0000100421"},
		{"0.000333123456", "0.000333123"},
		{"-0.00000003077", "-0.00000003077"},
		{"0.009999999999", "0.01"},
		{"0.0000002", "0.0000002"},
	}
	for _, c := range cases {
		got := roundForWire(decimal.RequireFromString(c.in)).String()
		if got != c.want {
			t.Errorf("roundForWire(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

// TestRoundForWire_MatchesRound6AtOrAboveThreshold guards the compatibility
// promise: nothing at or above 0.01 may change on the wire.
func TestRoundForWire_MatchesRound6AtOrAboveThreshold(t *testing.T) {
	for _, s := range []string{
		"0.01", "0.010000001", "0.0999999", "0.1", "0.5", "1", "1.0000005",
		"9.87654321", "56.253456789", "1234.5678", "99999.999999949",
		"-0.01", "-56.253456789", "-1234.56785",
	} {
		d := decimal.RequireFromString(s)
		if got, want := roundForWire(d).String(), d.Round(6).String(); got != want {
			t.Errorf("roundForWire(%s) = %s, want Round(6) = %s", s, got, want)
		}
	}
}

// TestRoundForWire_SignificantDigitsAgainstBigRat checks the sub-threshold
// branch against an independent reference: the result must keep 6 significant
// digits and stay within half an ulp of that grid.
func TestRoundForWire_SignificantDigitsAgainstBigRat(t *testing.T) {
	for _, s := range []string{
		"0.009", "0.0012345678", "0.000999999", "0.00000068464",
		"0.0000241235", "0.000000000123456789", "0.0000000000000001357911",
		"-0.0012345678", "-0.00000068464",
	} {
		d := decimal.RequireFromString(s)
		got := roundForWire(d)

		if sig := significantDigits(got); sig > 6 {
			t.Errorf("roundForWire(%s) = %s has %d significant digits, want <= 6", s, got, sig)
		}
		// |d - got| must not exceed half the place value of the last kept digit.
		exact, _ := new(big.Rat).SetString(s)
		half := new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(2))
		ulp := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Exp(
			big.NewInt(10), big.NewInt(int64(-got.Exponent())), nil))
		maxErr := new(big.Rat).Mul(ulp, half)

		gotRat, _ := new(big.Rat).SetString(got.String())
		diff := new(big.Rat).Sub(exact, gotRat)
		diff.Abs(diff)
		if diff.Cmp(maxErr) > 0 {
			t.Errorf("roundForWire(%s) = %s: error %s exceeds half-ulp %s",
				s, got, diff.FloatString(30), maxErr.FloatString(30))
		}
	}
}

// TestNum6_MarshalsAsValidJSONNumber: the smallest values must never leave
// as scientific notation, which is not a valid JSON number.
func TestNum6_MarshalsAsValidJSONNumber(t *testing.T) {
	for _, s := range []string{
		"0", "56.25", "0.00000068464", "0.000000000000000000000001", "-0.0000002",
		"1e-40", "1e20",
	} {
		b, err := json.Marshal(newNum6(decimal.RequireFromString(s)))
		if err != nil {
			t.Fatalf("marshal %s: %v", s, err)
		}
		if !json.Valid(b) {
			t.Errorf("marshal(%s) = %s, not valid JSON", s, b)
		}
		var f float64
		if err := json.Unmarshal(b, &f); err != nil {
			t.Errorf("marshal(%s) = %s, not a JSON number: %v", s, b, err)
		}
	}
}

func TestLeadingDigitPos(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"1", 0}, {"9.99", 0}, {"10", 1}, {"0.1", -1}, {"0.0999", -2},
		{"0.01", -2}, {"0.0000002", -7}, {"-0.0000002", -7}, {"123456", 5},
	}
	for _, c := range cases {
		if got := leadingDigitPos(decimal.RequireFromString(c.in)); got != c.want {
			t.Errorf("leadingDigitPos(%s) = %d, want %d", c.in, got, c.want)
		}
	}
	// Coefficient 10^15 is the case decimal.NumDigits() gets wrong.
	coef, _ := new(big.Int).SetString("1000000000000000", 10)
	if got := leadingDigitPos(decimal.NewFromBigInt(coef, -17)); got != -2 {
		t.Errorf("leadingDigitPos(0.01 as 10^15e-17) = %d, want -2", got)
	}
}

func significantDigits(d decimal.Decimal) int {
	s := d.Coefficient().String()
	if len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	for len(s) > 1 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if s == "0" {
		return 0
	}
	return len(s)
}

// The threshold must be exactly 0.01: a value just below it takes the
// significant-digit branch, a value at it keeps plain Round(6). Raising or
// lowering wireSmallValueThreshold breaks one of these.
func TestRoundForWire_ThresholdIsPinnedFromBothSides(t *testing.T) {
	// 0.009123456789 rounds differently under each rule (0.009123 vs
	// 0.00912346), so it detects the branch actually taken.
	justBelow := decimal.RequireFromString("0.009123456789")
	if got, round6 := roundForWire(justBelow).String(), justBelow.Round(6).String(); got == round6 {
		t.Errorf("just below the threshold must NOT use Round(6): got %s", got)
	}
	justAbove := decimal.RequireFromString("0.010123456789")
	if got, want := roundForWire(justAbove).String(), justAbove.Round(6).String(); got != want {
		t.Errorf("just above the threshold roundForWire = %s, want Round(6) = %s", got, want)
	}
}
