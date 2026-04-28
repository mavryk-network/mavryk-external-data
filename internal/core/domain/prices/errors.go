package prices

import (
	"errors"
	"fmt"
)

// ErrTokenNotFound is returned when an FT token symbol is not in the registry.
// HTTP layer maps this to 404; never inspect message strings.
var ErrTokenNotFound = errors.New("token not found")

// ErrPairNotFound is the RWA equivalent.
var ErrPairNotFound = errors.New("rwa pair not found")

// ErrSourceNotFound is for unknown source codes (rare; sources are seeded).
var ErrSourceNotFound = errors.New("source not found")

// PairAmbiguousError signals that >1 enabled rwa_pairs row matches the same
// (base_symbol, quote_symbol) tuple. Schema-wise this is allowed (the natural
// key is (source_code, orderbook_addr), not (base, quote)) — in practice it's
// an operator-side state that needs manual resolution. HTTP layer maps this
// to 409 with the conflicting pair_ids in the response details.
type PairAmbiguousError struct {
	Base, Quote string
	IDs         []int64
}

func (e *PairAmbiguousError) Error() string {
	return fmt.Sprintf("rwa pair ambiguous for %s-%s: %d enabled rows", e.Base, e.Quote, len(e.IDs))
}
