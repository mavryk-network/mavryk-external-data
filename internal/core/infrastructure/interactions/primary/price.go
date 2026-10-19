package primary

import "github.com/shopspring/decimal"

func humanPrice(raw string, decimals int) (string, bool) {
	n, err := decimal.NewFromString(raw)
	if err != nil || !n.IsPositive() || !n.Equal(n.Truncate(0)) || decimals < 0 || decimals > 40 {
		return "", false
	}
	return n.Shift(-int32(decimals)).String(), true
}
