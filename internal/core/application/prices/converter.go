package prices

import (
	"context"
	"errors"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// Sentinel errors returned by PriceConverter.Convert. The HTTP layer maps
// them into the `fx.error` field of a 200 response (partial success), not
// 4xx/5xx — see rwa_quotes_adds.md §4.4.
var (
	ErrNoFXRate                  = errors.New("no fx rate available")
	ErrUnsupportedTargetCurrency = errors.New("unsupported target currency")
	ErrSourceTokenNotRegistered  = errors.New("source token not in registry")
)

// ConversionResult carries the converted amount along with the metadata
// the API layer surfaces in `fx`. Stale=true means the rate is older than
// the configured staleness budget but still served.
type ConversionResult struct {
	Amount decimal.Decimal // sourceAmount * Rate, rounded to 18 decimal places
	Rate   decimal.Decimal // FX rate at RateTS
	Source prices.Source   // typically prices.SourceCoinGecko
	RateTS time.Time
	Stale  bool

	// Identity is true when source token == target currency (e.g. USDT→USDT
	// for a pair quoted in USDT, ?in=usdt). Rate=1.0, no upstream query.
	Identity bool
}

// PriceConverter computes target-currency-amounts from a source-token-amount
// using the FT-side `token_prices` series as the FX-rate source.
//
// Contract:
//   - target must be in prices.AllSupportedCurrencies(); otherwise
//     ErrUnsupportedTargetCurrency.
//   - sourceToken must be registered in the runtime token registry
//     (typically the FT-side `tokens` table); otherwise
//     ErrSourceTokenNotRegistered.
//   - When sourceToken == target (case-insensitive), returns Identity=true
//     with Rate=1.0 — no upstream lookup.
//   - Otherwise, looks up the latest `token_prices` row for
//     (sourceToken, target). If none is available, returns ErrNoFXRate.
//   - Stale=true when (now - RateTS) > maxStaleness.
type PriceConverter interface {
	Convert(
		ctx context.Context,
		sourceToken prices.Token,
		target prices.Currency,
		sourceAmount decimal.Decimal,
		ts time.Time,
	) (ConversionResult, error)
}
