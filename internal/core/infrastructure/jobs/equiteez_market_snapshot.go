package jobs

import (
	"context"
	"strings"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"github.com/shopspring/decimal"
)

type marketSnapshotWriter interface {
	SaveSnapshots(context.Context, []prices.RWAMarketSnapshot) error
}

// Every requested pair gets a complete observation, including missing/empty
// books. Otherwise a vanished side could survive as a falsely usable quote.
func buildMarketSnapshots(tokens []equiteez.TokenWithOrderbooks, pairs []prices.RWAPair, decimals map[int64]int, network, generation string, now time.Time) []prices.RWAMarketSnapshot {
	out := make([]prices.RWAMarketSnapshot, 0, len(pairs))
	for _, pair := range pairs {
		s := prices.RWAMarketSnapshot{PairID: pair.ID, Network: network, IndexerGeneration: generation,
			ObservedAt: now, TokenAddr: pair.TokenAddr, QuoteAddr: pair.QuoteAddr,
			OrderbookAddr: pair.OrderbookAddr, NativeQuote: strings.ToLower(pair.QuoteSymbol), Reason: "orderbook_missing"}
		var token *equiteez.TokenWithOrderbooks
		var book *equiteez.EquiteezOrderbook
		matches := 0
		for i := range tokens {
			if tokens[i].Address != pair.TokenAddr {
				continue
			}
			for j := range tokens[i].Orderbooks {
				if tokens[i].Orderbooks[j].Address == pair.OrderbookAddr {
					token, book = &tokens[i], &tokens[i].Orderbooks[j]
					matches++
				}
			}
		}
		if matches > 1 {
			s.Reason = "ambiguous_identity"
		}
		if matches == 1 {
			// USD with no token is the indexer's sell-order label, not another
			// payment currency. Never discard an unresolved actual currency.
			currencies := make([]equiteez.OrderbookCurrency, 0, len(book.Currencies))
			for _, c := range book.Currencies {
				if c.Token == nil && c.CurrencyName == "USD" {
					continue
				}
				currencies = append(currencies, c)
			}
			if token.TokenIDKnown {
				id := token.TokenID
				s.TokenID = &id
			}
			switch {
			case !token.TokenIDKnown || token.TokenID != 0:
				s.Reason = "unsupported_token_id"
			case len(currencies) != 1:
				s.Reason = "ambiguous_quote"
			default:
				currency := currencies[0]
				quote := currency.Token
				if quote != nil && quote.TokenIDKnown {
					id := quote.TokenID
					s.QuoteTokenID = &id
				}
				switch {
				case quote == nil || !quote.TokenIDKnown || quote.TokenID != 0 || quote.Address == "" ||
					quote.Address != pair.QuoteAddr || !strings.EqualFold(strings.TrimSpace(currency.CurrencyName), pair.QuoteSymbol):
					s.Reason = "quote_identity_mismatch"
				default:
					d, known := decimals[pair.ID]
					if !known || d < 0 || d > 40 {
						s.Reason = "quote_decimals_unknown"
						break
					}
					s.Reason = ""
					shift := -int32(d) //nolint:gosec // bounded above
					s.Bid, s.BidExpiresAt = liveLimitPrice(book.PricingBids, 0, shift, now)
					s.Ask, s.AskExpiresAt = liveLimitPrice(book.PricingAsks, 1, shift, now)
					if v, _, ok := mappablePrice(book.LastMatchedPrice, shift); ok {
						s.LastPrice = &v
					}
					if ts, err := time.Parse(time.RFC3339Nano, book.LastMatchedPriceTimestamp); err == nil && !ts.IsZero() {
						ts = ts.UTC()
						s.LastTradeAt = &ts
					}
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// Fail closed if the selected order changed eligibility or expired while the
// request was in flight. Null expiry follows the indexer's no-deadline rule.
func liveLimitPrice(rows []equiteez.PricingLimitOrder, side int, shift int32, now time.Time) (*decimal.Decimal, *time.Time) {
	if len(rows) != 1 {
		return nil, nil
	}
	o := rows[0]
	if o.OrderType != side || !o.Remaining.Decimal().IsPositive() || o.Fulfilled || o.Canceled || o.Expired || o.Refunded || o.Market ||
		(o.ExpiresAt != nil && !now.Before(*o.ExpiresAt)) {
		return nil, nil
	}
	price, _, ok := mappablePrice(o.Price, shift)
	if !ok {
		return nil, nil
	}
	return &price, o.ExpiresAt
}
