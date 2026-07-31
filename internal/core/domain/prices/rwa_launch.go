package prices

import (
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Launchpad status codes as emitted by the indexer's `launchpad_launch.status`.
// Mirrors the mapping used by mavryk-rwa-backend so both services badge an asset
// identically.
const (
	LaunchStatusActive   = 0
	LaunchStatusInactive = 1
	LaunchStatusPaused   = 2
	LaunchStatusClosed   = 3
)

// LaunchStatusString renders a raw status code as the wire string.
func LaunchStatusString(code int) string {
	switch code {
	case LaunchStatusActive:
		return "active"
	case LaunchStatusInactive:
		return "inactive"
	case LaunchStatusPaused:
		return "paused"
	case LaunchStatusClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// RWALaunch is the surfaced primary-issuance state of one RWA token: the asset
// is being sold at a fixed tier price rather than traded on an orderbook, so it
// carries a sale price + progress instead of a market quote and candles.
//
// Amounts stay decimal (not float) because raw on-chain nats are supply-scale
// and would lose precision as float64; Price is already decimals-applied and
// value-bounded.
type RWALaunch struct {
	Source    Source
	TokenAddr string
	TokenID   int
	LaunchID  int
	Name      string
	Status    string // active | inactive | paused | closed
	Active    bool   // purchasable right now
	// QuoteAddr is the on-chain contract of the quote (payment) token, taken
	// from the same sale-option payment that sets Price — so price, symbol and
	// address always describe one payment row. Empty until synced.
	QuoteAddr   string
	BaseSymbol  string
	QuoteSymbol string

	Price           decimal.Decimal // base-tier price per token, in QuoteSymbol units
	TotalBought     decimal.Decimal // raw nat
	MaxAmountCap    decimal.Decimal // raw nat
	ProgressPercent float64

	SaleStart  *time.Time
	SaleEnd    *time.Time
	SaleClosed *time.Time

	// LastSyncedAt is when the launchpad was last read. A sale price is a fixed
	// quote rather than a market observation, so this is the honest "as of" to
	// surface: it tells the client how fresh our copy is.
	LastSyncedAt time.Time

	Enabled bool
}

// Symbol renders the canonical `{base}-{quote}` used across the RWA API, so a
// primary-issuance asset is addressable exactly like an orderbook pair.
func (l RWALaunch) Symbol() string {
	return strings.ToLower(l.BaseSymbol) + "-" + strings.ToLower(l.QuoteSymbol)
}

// LaunchSelectable is the minimal projection needed to choose which launch
// represents a token and whether it is currently purchasable. Kept free of any
// transport/storage types so the sync job and tests share one implementation.
type LaunchSelectable struct {
	Status     int
	IsPaused   bool
	SaleStart  *time.Time
	SaleEnd    *time.Time
	SaleClosed *time.Time
	UpdatedAt  time.Time
}

// LaunchActive reports whether a launch is purchasable at `now`: status active,
// not paused, not closed, and inside the sale window. Absent bounds are treated
// as open-ended — a launch with no sale_end never expires on its own.
func LaunchActive(l LaunchSelectable, now time.Time) bool {
	if l.Status != LaunchStatusActive || l.IsPaused || l.SaleClosed != nil {
		return false
	}
	if l.SaleStart != nil && now.Before(*l.SaleStart) {
		return false
	}
	if l.SaleEnd != nil && now.After(*l.SaleEnd) {
		return false
	}
	return true
}

// SelectLaunch picks the launch that represents a token when several exist
// (re-issuance, history): an active one wins, otherwise the most recently
// updated. Returns the winning index and ok=false for an empty slice.
func SelectLaunch(rows []LaunchSelectable, now time.Time) (int, bool) {
	best := -1
	for i, r := range rows {
		switch {
		case best < 0:
			best = i
		case LaunchActive(r, now) && !LaunchActive(rows[best], now):
			best = i // an active launch always beats an inactive one
		case LaunchActive(r, now) == LaunchActive(rows[best], now) && r.UpdatedAt.After(rows[best].UpdatedAt):
			best = i
		}
	}
	return best, best >= 0
}

// ProgressPercent computes total_bought / max_amount_cap * 100 from raw-nat
// strings. Both operands are in the same token units, so decimals cancel and we
// never need to know them. Returns 0 when the cap is zero/empty or either side
// does not parse — a missing cap must not produce NaN or panic.
//
// big.Float keeps supplies beyond float64's 2^53 exact; the result is rounded to
// 10 decimals so a barely-started launch stays visible (2.667e-7) instead of
// collapsing to 0, while trimming the long mantissa.
func ProgressPercent(totalBoughtRaw, maxCapRaw string) float64 {
	total, ok1 := new(big.Float).SetPrec(256).SetString(strings.TrimSpace(totalBoughtRaw))
	capacity, ok2 := new(big.Float).SetPrec(256).SetString(strings.TrimSpace(maxCapRaw))
	if !ok1 || !ok2 || capacity.Sign() <= 0 {
		return 0
	}
	ratio := new(big.Float).Quo(total, capacity)
	ratio.Mul(ratio, big.NewFloat(100))
	f, _ := ratio.Float64()
	return math.Round(f*1e10) / 1e10
}

// maxLaunchDecimals bounds the 10^decimals scaling. Raw values are on-chain
// nats; anything past 40 decimals is indistinguishable from zero, while the
// exponentiation cost grows with the exponent — so a corrupt decimals value
// cannot burn CPU in the sync path.
const maxLaunchDecimals = 40

// LaunchHumanPrice converts a raw payment price to human units by dividing by
// 10^decimals (e.g. 100000000 raw USDT with 6 decimals → 100). Returns
// (zero, false) when the raw value is unparseable or decimals is out of range,
// so the caller can skip the launch rather than publish a bogus price.
func LaunchHumanPrice(raw string, decimals int) (decimal.Decimal, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || decimals < 0 || decimals > maxLaunchDecimals {
		return decimal.Decimal{}, false
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Decimal{}, false
	}
	//nolint:gosec // decimals is range-checked above; the conversion cannot overflow
	return v.Shift(-int32(decimals)), true
}
