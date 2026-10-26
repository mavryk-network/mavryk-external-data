package handlers

import (
	"context"

	"quotes/internal/core/api/http/common"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
)

// PricingBySymbol is additive: the old /latest still means last observed trade
// price, while /pricing can return a midpoint even before the first trade.
func (d RWAPriceDeps) PricingBySymbol() gin.HandlerFunc {
	action := func(ctx context.Context, target rwaTarget) (apiprices.RWAPricing, error) {
		var pair *prices.RWAPair
		if target.HasPair {
			pair = &target.Pair
		}
		service := d.Pricing
		if service == nil {
			service = &apiprices.RWAPricingService{Network: "basenet"}
		}
		return service.Get(ctx, pair, target.Launch)
	}
	return common.Wrap(d.resolveTargetFromPath, action)
}
