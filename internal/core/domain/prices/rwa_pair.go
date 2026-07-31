package prices

import (
	"strconv"
	"time"
)

// RWAPair is the lookup row for an RWA orderbook pair (matches `rwa_pairs` in
// DB). It is identity + cache: the synthetic ID is the stable handle used by
// `rwa_quote_prices.pair_id` and the HTTP API; the source-of-truth metadata
// (orderbook contract, allowlist status, etc.) is owned by the upstream
// indexer and refreshed by the discovery sync.
//
// Fields:
//   - ID:                  BIGSERIAL — stable across restarts and deploys.
//   - Source:               registry source (`equiteez` today).
//   - TokenAddr:             Tezos token contract that owns the orderbook.
//   - QuoteAddr:             Tezos contract of the quote token (e.g. USDT) —
//     what a consumer building transactions approves/spends against. Empty
//     until the first discovery sync after migration 0017.
//   - OrderbookAddr:         Tezos orderbook contract — what the collector polls.
//   - EquiteezOrderbookID:   indexer's internal integer ID for the orderbook
//     (Hasura `orderbook.id`). Required by the Equiteez backfill job to query
//     `orderbook_order` events efficiently; nil until SyncRWAPairs runs.
//   - BaseSymbol:           human label for the base asset (token).
//   - QuoteSymbol:          human label for the quote currency (e.g. "USDT").
//   - Enabled:              local override; the sync respects this flag (won't
//     re-enable an operator-disabled pair).
//   - LastSyncedAt:         audit trail.
type RWAPair struct {
	ID                  int64
	Source              Source
	TokenAddr           string
	QuoteAddr           string
	OrderbookAddr       string
	EquiteezOrderbookID *int32
	BaseSymbol          string
	QuoteSymbol         string
	Enabled             bool
	LastSyncedAt        *time.Time
}

// EntityKey is the canonical PricePoint.EntityKey for this pair (decimal
// string of the synthetic ID).
func (p RWAPair) EntityKey() string {
	return strconv.FormatInt(p.ID, 10)
}
