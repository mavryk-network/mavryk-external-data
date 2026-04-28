package prices

import (
	"fmt"
	"strings"
)

// Currency is a quote currency (USD, EUR, BTC, ...). FT-quotes use it as the
// `metric` column on token_prices.
type Currency string

const (
	CurrencyUSD Currency = "usd"
	CurrencyEUR Currency = "eur"
	CurrencyBTC Currency = "btc"
	CurrencyETH Currency = "eth"
	CurrencyGBP Currency = "gbp"
	CurrencyCNY Currency = "cny"
	CurrencyJPY Currency = "jpy"
	CurrencyKRW Currency = "krw"
	CurrencyRUB Currency = "rub"
	CurrencyAED Currency = "aed"
)

// supportedCurrencies is the open set of currencies the FT-side knows how to
// fetch from CoinGecko. Adding a new currency = one line here + one line in the
// registry below + zero migrations.
var supportedCurrencies = map[Currency]struct{}{
	CurrencyUSD: {},
	CurrencyEUR: {},
	CurrencyBTC: {},
	CurrencyETH: {},
	CurrencyGBP: {},
	CurrencyCNY: {},
	CurrencyJPY: {},
	CurrencyKRW: {},
	CurrencyRUB: {},
	CurrencyAED: {},
}

// NewCurrency returns the canonical Currency for s, or an error if unsupported.
func NewCurrency(s string) (Currency, error) {
	c := Currency(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := supportedCurrencies[c]; !ok {
		return "", fmt.Errorf("unsupported currency: %q", s)
	}
	return c, nil
}

// MustNewCurrency is the bootstrap-time variant; panics on unknown.
func MustNewCurrency(s string) Currency {
	c, err := NewCurrency(s)
	if err != nil {
		panic(err)
	}
	return c
}

// String implements fmt.Stringer.
func (c Currency) String() string { return string(c) }

// AllSupportedCurrencies returns the registry as a deterministic slice.
// Used by the live job to enumerate currencies to collect.
func AllSupportedCurrencies() []Currency {
	out := make([]Currency, 0, len(supportedCurrencies))
	// Listed in the same order as the constants above so logs/metrics labels are stable.
	for _, c := range []Currency{
		CurrencyUSD, CurrencyEUR, CurrencyBTC, CurrencyETH, CurrencyGBP,
		CurrencyCNY, CurrencyJPY, CurrencyKRW, CurrencyRUB, CurrencyAED,
	} {
		if _, ok := supportedCurrencies[c]; ok {
			out = append(out, c)
		}
	}
	return out
}
