package equiteez

import (
	"strings"
)

// NormalizedUSDPerTokenFromOrderbook returns human USD (or USDT-pegged) per 1 token from one Equiteez orderbook.
// USDT quote amounts are divided by 1e6 (micro-USDT).
func NormalizedUSDPerTokenFromOrderbook(ob *EquiteezOrderbook) (usd float64, ok bool) {
	if ob == nil {
		return 0, false
	}
	raw := ob.LastMatchedPrice.Float64()
	if raw <= 0 {
		return 0, false
	}
	quote := firstOrderbookCurrencyName(ob)
	if strings.EqualFold(strings.TrimSpace(quote), "USDT") {
		raw /= 1_000_000
	}
	return raw, true
}

func firstOrderbookCurrencyName(ob *EquiteezOrderbook) string {
	if ob == nil || len(ob.Currencies) == 0 {
		return ""
	}
	return ob.Currencies[0].CurrencyName
}
