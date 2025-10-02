package quotes

import (
	"encoding/json"
	"time"
)

type Quote struct {
	Timestamp time.Time `json:"timestamp"`
	BTC       float64   `json:"btc"`
	USD       float64   `json:"usd"`
	EUR       float64   `json:"eur"`
	CNY       float64   `json:"cny"`
	JPY       float64   `json:"jpy"`
	KRW       float64   `json:"krw"`
	ETH       float64   `json:"eth"`
	GBP       float64   `json:"gbp"`
}

// MarshalJSON customizes JSON marshaling to ensure timestamp is in UTC format
func (q Quote) MarshalJSON() ([]byte, error) {
	type Alias Quote
	return json.Marshal(&struct {
		Timestamp string `json:"timestamp"`
		*Alias
	}{
		Timestamp: q.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		Alias:     (*Alias)(&q),
	})
}

type Currency string

const (
	CurrencyBTC Currency = "btc"
	CurrencyUSD Currency = "usd"
	CurrencyEUR Currency = "eur"
	CurrencyCNY Currency = "cny"
	CurrencyJPY Currency = "jpy"
	CurrencyKRW Currency = "krw"
	CurrencyETH Currency = "eth"
	CurrencyGBP Currency = "gbp"
)

func GetSupportedCurrencies() []Currency {
	return []Currency{
		CurrencyBTC,
		CurrencyUSD,
		CurrencyEUR,
		CurrencyCNY,
		CurrencyJPY,
		CurrencyKRW,
		CurrencyETH,
		CurrencyGBP,
	}
}
