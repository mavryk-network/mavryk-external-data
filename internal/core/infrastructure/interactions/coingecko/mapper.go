package coingecko

import (
	"quotes/internal/core/domain/quotes"
	"time"
)

type PriceData struct {
	Timestamp time.Time
	Price     float64
}

// MapToQuotes converts CoinGecko API response to domain quotes
// It normalizes data to seconds using forward-fill strategy
func MapToQuotes(currencyData map[string]*MarketChartRangeResponse) ([]quotes.Quote, error) {
	if len(currencyData) == 0 {
		return nil, nil
	}

	// Get all unique timestamps and sort them
	timestampMap := make(map[int64]bool)
	for _, data := range currencyData {
		for _, price := range data.Prices {
			if len(price) >= 2 {
				timestampMap[int64(price[0]/1000)] = true // Convert from milliseconds to seconds
			}
		}
	}

	// Convert to sorted slice
	var timestamps []int64
	for ts := range timestampMap {
		timestamps = append(timestamps, ts)
	}

	// Sort timestamps
	for i := 0; i < len(timestamps); i++ {
		for j := i + 1; j < len(timestamps); j++ {
			if timestamps[i] > timestamps[j] {
				timestamps[i], timestamps[j] = timestamps[j], timestamps[i]
			}
		}
	}

	// Create price maps for each currency
	priceMaps := make(map[string]map[int64]float64)
	for currency, data := range currencyData {
		priceMap := make(map[int64]float64)
		for _, price := range data.Prices {
			if len(price) >= 2 {
				timestamp := int64(price[0] / 1000) // Convert to seconds
				priceMap[timestamp] = price[1]
			}
		}
		priceMaps[currency] = priceMap
	}

	// Create quotes with forward-fill
	var result []quotes.Quote
	var lastQuote *quotes.Quote

	for _, timestamp := range timestamps {
		quote := quotes.Quote{
			Timestamp: time.Unix(timestamp, 0).UTC(),
		}

		// Fill prices for each currency using forward-fill
		for _, currency := range quotes.GetSupportedCurrencies() {
			currencyStr := string(currency)
			if priceMap, exists := priceMaps[currencyStr]; exists {
				if price, exists := priceMap[timestamp]; exists {
					// Use actual price
					setQuotePrice(&quote, currency, price)
				} else if lastQuote != nil {
					// Forward-fill from last quote
					setQuotePrice(&quote, currency, getQuotePrice(*lastQuote, currency))
				}
			}
		}

		// Only add quote if it has at least one price
		if hasAnyPrice(quote) {
			result = append(result, quote)
			lastQuote = &quote
		}
	}

	return result, nil
}

func setQuotePrice(quote *quotes.Quote, currency quotes.Currency, price float64) {
	switch currency {
	case quotes.CurrencyBTC:
		quote.BTC = price
	case quotes.CurrencyUSD:
		quote.USD = price
	case quotes.CurrencyEUR:
		quote.EUR = price
	case quotes.CurrencyCNY:
		quote.CNY = price
	case quotes.CurrencyJPY:
		quote.JPY = price
	case quotes.CurrencyKRW:
		quote.KRW = price
	case quotes.CurrencyETH:
		quote.ETH = price
	case quotes.CurrencyGBP:
		quote.GBP = price
	}
}

func getQuotePrice(quote quotes.Quote, currency quotes.Currency) float64 {
	switch currency {
	case quotes.CurrencyBTC:
		return quote.BTC
	case quotes.CurrencyUSD:
		return quote.USD
	case quotes.CurrencyEUR:
		return quote.EUR
	case quotes.CurrencyCNY:
		return quote.CNY
	case quotes.CurrencyJPY:
		return quote.JPY
	case quotes.CurrencyKRW:
		return quote.KRW
	case quotes.CurrencyETH:
		return quote.ETH
	case quotes.CurrencyGBP:
		return quote.GBP
	default:
		return 0
	}
}

func hasAnyPrice(quote quotes.Quote) bool {
	return quote.BTC != 0 || quote.USD != 0 || quote.EUR != 0 ||
		quote.CNY != 0 || quote.JPY != 0 || quote.KRW != 0 ||
		quote.ETH != 0 || quote.GBP != 0
}
