package tickers

import "strings"

// dexIdentifiers is the hard-coded allowlist of CoinGecko market.identifier
// values that should be classified as DEX. Everything else falls through to
// CEX (the safe default — misclassifying a DEX as CEX is a one-line fix; the
// other direction is harder to spot).
//
// Sourced from CG's /exchanges?per_page=250 dump, filtered by centralized=false
// (see https://api.coingecko.com/api/v3/exchanges). Update by appending; no
// schema change.
var dexIdentifiers = map[string]struct{}{
	"uniswap_v2":         {},
	"uniswap_v3":         {},
	"uniswap_v4":         {},
	"sushiswap":          {},
	"pancakeswap_new":    {},
	"pancakeswap":        {},
	"pancakeswap_v3":     {},
	"curve":              {},
	"balancer":           {},
	"raydium":            {},
	"raydium2":           {},
	"orca":               {},
	"jupiter_aggregator": {},
	"jupiter":            {},
	"quickswap":          {},
	"trader_joe":         {},
	"camelot":            {},
	"velodrome":          {},
	"aerodrome":          {},
	"meteora":            {},
	"thorchain":          {},
	"dodo":               {},
	"hyperliquid":        {},
	"plenty_network":     {},
	"quipuswap":          {},
}

// ClassifyExchangeKind returns DEX when id is in the allowlist, otherwise CEX.
// Case-insensitive on the input.
func ClassifyExchangeKind(id string) ExchangeKind {
	if _, ok := dexIdentifiers[strings.ToLower(strings.TrimSpace(id))]; ok {
		return ExchangeKindDEX
	}
	return ExchangeKindCEX
}
