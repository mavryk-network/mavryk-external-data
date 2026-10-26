package prices

import (
	"time"

	"github.com/shopspring/decimal"
)

// RWAMarketSnapshot is one complete upstream observation. A missing side is
// data, not permission to reuse a previous observation's side.
type RWAMarketSnapshot struct {
	PairID            int64
	Network           string
	IndexerGeneration string
	ObservedAt        time.Time
	TokenAddr         string
	TokenID           *int
	QuoteAddr         string
	QuoteTokenID      *int
	OrderbookAddr     string
	NativeQuote       string
	Reason            string
	Bid               *decimal.Decimal
	Ask               *decimal.Decimal
	BidExpiresAt      *time.Time
	AskExpiresAt      *time.Time
	LastPrice         *decimal.Decimal
	LastTradeAt       *time.Time
}
