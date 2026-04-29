package prices

import (
	"fmt"
	"strings"
)

// Side is the orderbook metric for an RWA quote. RWA-quotes use it as the
// `metric` (`side` column) on rwa_quote_prices.
type Side string

const (
	SideBid  Side = "bid"
	SideAsk  Side = "ask"
	SideLast Side = "last"
	SideMid  Side = "mid"
)

var supportedSides = map[Side]struct{}{
	SideBid: {}, SideAsk: {}, SideLast: {}, SideMid: {},
}

// NewSide returns the canonical Side or an error.
func NewSide(s string) (Side, error) {
	v := Side(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := supportedSides[v]; !ok {
		return "", fmt.Errorf("unsupported orderbook side: %q", s)
	}
	return v, nil
}

// String implements fmt.Stringer.
func (s Side) String() string { return string(s) }
