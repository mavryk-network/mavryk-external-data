package jobs

import (
	"context"
	"errors"
	"github.com/shopspring/decimal"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"strconv"
	"time"
)

// valueRoute is shared by live and historical valuation. Pair presence is
// routing evidence, even when last is missing: it never falls back to primary.
func (j *RWASupplyJob) valueRoute(ctx context.Context, s valuation.Supply, scope equiteez.AllowlistSnapshot, route valuation.Point, at time.Time, ambiguous bool) (valuation.Point, error) {
	p := valuation.Point{Symbol: route.Symbol, Quote: route.Quote, PairID: route.PairID, Orderbook: route.Orderbook, Generation: route.Generation, PriceBasis: route.PriceBasis, AsOf: at}
	id := s.Identity
	reason := ""
	switch {
	case ambiguous:
		reason = "identity_ambiguous"
	case p.PairID != 0:
		market, ok, err := j.Store.MarketAt(ctx, p.PairID, at)
		if err != nil {
			return p, err
		}
		switch {
		case !ok:
			reason = "price_missing"
		case market.TokenID == nil || market.QuoteTokenID == nil || market.TokenAddr != id.Contract || strconv.Itoa(*market.TokenID) != id.TokenID || market.QuoteAddr != p.Quote.Contract || market.OrderbookAddr != p.Orderbook || market.Network != id.Network || market.IndexerGeneration != p.Generation:
			reason = "identity_mismatch"
		case !scope.Document.PairAllowed(id.Contract, p.Quote.Contract, p.Orderbook):
			reason = "routing_unverified"
		default:
			p.Quote.TokenID = strconv.Itoa(*market.QuoteTokenID)
			p.Generation = market.IndexerGeneration
			p.Price = market.LastPrice
			p.PriceAt = market.ObservedAt
			p.LastTradeAt = market.LastTradeAt
			reason = market.Reason
		}
	case j.Primary == nil:
		reason = "price_missing"
	default:
		terms, err := j.Primary.ListingTerms(ctx, app.PrimaryRequest{Network: id.Network, Contract: id.Contract, TokenID: id.TokenID, Level: s.Block.Level, At: s.Block.Timestamp, Mode: app.PrimaryHoldings})
		switch {
		case err != nil:
			reason = "price_missing"
			var u *app.PrimaryUnavailable
			if errors.As(err, &u) {
				reason = u.Reason
			}
		case !scope.Document.LaunchAllowed(id.Contract, terms.PaymentContract, terms.Launchpad):
			reason = "routing_unverified"
		default:
			price, e := decimal.NewFromString(terms.Price)
			if e != nil {
				return p, e
			}
			p.Price = &price
			p.PriceAt = s.Block.Timestamp
			p.Launchpad = terms.Launchpad
			p.SaleStatus, p.SalePaused = terms.SaleStatus, &terms.SalePaused
			p.Quote = valuation.Quote{Contract: terms.PaymentContract, TokenID: terms.PaymentTokenID, Symbol: "usdt"}
		}
	}
	p = valuation.Calculate(s, p, j.Config.RWASupply.WithDefaults(id.Network).Policy())
	if reason != "" {
		p.PriceQuality = valuation.Missing(reason)
		p.MarketCap = nil
		p.FDV = nil
		p.MarketCapQuality = valuation.Missing(reason)
		p.FDVQuality = valuation.Missing(reason)
	}
	j.convert(ctx, &p)
	return p, nil
}
