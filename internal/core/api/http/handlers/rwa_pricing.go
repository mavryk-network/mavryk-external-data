package handlers

import (
	"context"
	"strings"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/logging"
)

func (d RWAPriceDeps) pricingService() *apiprices.RWAPricingService {
	if d.Pricing != nil {
		return d.Pricing
	}
	return &apiprices.RWAPricingService{Network: "basenet", Converter: d.Converter}
}

func (d RWAPriceDeps) currentPricing(ctx context.Context, target rwaTarget) (*apiprices.RWAPricing, error) {
	var pair *prices.RWAPair
	if target.HasPair {
		pair = &target.Pair
	}
	q, err := d.pricingService().Get(ctx, pair, target.Launch)
	if err != nil {
		logPricingReadError(ctx, err)
		return nil, nil // optional enrichment must not suppress legacy prices
	}
	return &q, nil
}

func emptyLegacyPrice(pair prices.RWAPair) rwaPriceDTO {
	return rwaPriceDTO{NativeQuote: strings.ToLower(pair.QuoteSymbol), Market: marketSecondary, PriceUnavailable: true}
}

func logPricingReadError(ctx context.Context, err error) {
	logging.LoggerFromContext(ctx, nil).Warn().Err(err).Msg("rwa_pricing_enrichment_unavailable")
}
