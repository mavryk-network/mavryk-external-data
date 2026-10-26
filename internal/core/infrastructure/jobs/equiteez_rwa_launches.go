package jobs

import (
	"strings"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"

	"github.com/shopspring/decimal"
)

// defaultLaunchQuoteDecimals is the fallback when the payment currency is not in
// the token registry. The launchpad quotes in USDT-class stablecoins (6
// decimals) and the orderbook read path makes the same assumption for an
// unregistered quote side — a wrong guess only mis-scales a display price, and
// the alternative (dropping the asset) hides it from the catalog entirely.
const defaultLaunchQuoteDecimals = 6

// groupLaunchesByToken buckets rows by token address, dropping rows with no
// token reference (the join is nullable in the indexer schema).
func groupLaunchesByToken(rows []equiteez.LaunchRow) map[string][]equiteez.LaunchRow {
	out := make(map[string][]equiteez.LaunchRow)
	for _, r := range rows {
		if r.Token == nil {
			continue
		}
		addr := strings.TrimSpace(r.Token.Address)
		if addr == "" {
			continue
		}
		out[addr] = append(out[addr], r)
	}
	return out
}

// buildLaunch selects the surfaced launch for one token and maps it to the
// domain type: base-tier price (decimals applied) plus raw sale progress.
//
// ok=false when the token has no launch quoting a usable price — storing a
// priceless asset would surface a null-price row in the catalog for no gain.
func buildLaunch(rows []equiteez.LaunchRow, baseSymbol string, now time.Time) (prices.RWALaunch, bool) {
	if len(rows) == 0 {
		return prices.RWALaunch{}, false
	}
	sel := make([]prices.LaunchSelectable, len(rows))
	for i, r := range rows {
		sel[i] = prices.LaunchSelectable{
			Status:     r.Status,
			IsPaused:   r.IsPaused,
			SaleStart:  r.SaleStart.Ptr(),
			SaleEnd:    r.SaleEnd.Ptr(),
			SaleClosed: r.SaleClosed.Ptr(),
			UpdatedAt:  r.UpdatedAt.Value(),
		}
	}
	idx, ok := prices.SelectLaunch(sel, now)
	if !ok {
		return prices.RWALaunch{}, false
	}
	row := rows[idx]

	rawPrice, currency, quoteAddr, hasPrice := row.BaseTierPrice()
	if !hasPrice {
		return prices.RWALaunch{}, false
	}
	quoteSymbol := strings.ToLower(strings.TrimSpace(currency))
	decimals, known := lookupQuoteDecimals(quoteSymbol)
	if !known {
		decimals = defaultLaunchQuoteDecimals
	}
	price, priceOK := prices.LaunchHumanPrice(rawPrice, decimals)
	if !priceOK {
		return prices.RWALaunch{}, false
	}
	var pricingQuoteTokenID *int
	if known {
		pricingQuoteTokenID = row.PricingQuoteTokenID(quoteSymbol, quoteAddr)
	}

	tokenID := 0
	addr := ""
	if row.Token != nil {
		tokenID = row.Token.TokenID
		addr = strings.TrimSpace(row.Token.Address)
	}

	return prices.RWALaunch{
		Source:              prices.SourceEquiteez,
		TokenAddr:           addr,
		TokenID:             tokenID,
		LaunchID:            row.ID,
		Name:                row.Name,
		Status:              prices.LaunchStatusString(row.Status),
		Active:              prices.LaunchActive(sel[idx], now),
		BaseSymbol:          strings.ToLower(strings.TrimSpace(baseSymbol)),
		QuoteSymbol:         quoteSymbol,
		QuoteAddr:           quoteAddr,
		PricingQuoteTokenID: pricingQuoteTokenID,
		Price:               price,
		TotalBought:         decimalOrZero(row.TotalBought.String()),
		MaxAmountCap:        decimalOrZero(row.MaxAmountCap.String()),
		ProgressPercent:     prices.ProgressPercent(row.TotalBought.String(), row.MaxAmountCap.String()),
		SaleStart:           row.SaleStart.Ptr(),
		SaleEnd:             row.SaleEnd.Ptr(),
		SaleClosed:          row.SaleClosed.Ptr(),
		Enabled:             true,
	}, true
}

// decimalOrZero parses a raw-nat string, yielding zero for empty/malformed
// input. Raw amounts are informational (progress is precomputed), so a bad
// value must not drop the asset from the catalog.
func decimalOrZero(raw string) decimal.Decimal {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return v
}
