package prices

import (
	"fmt"
	"strings"
)

// Source is the upstream provider key (matches sources.code in DB).
// Validated against the sources registry; never construct directly.
type Source string

const (
	SourceCoinGecko Source = "coingecko"
	SourceEquiteez  Source = "equiteez"
)

var sourceRegistry = map[Source]struct{}{
	SourceCoinGecko: {},
	SourceEquiteez:  {},
}

// NewSource returns the canonical Source for s, or an error if unsupported.
func NewSource(s string) (Source, error) {
	src := Source(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := sourceRegistry[src]; !ok {
		return "", fmt.Errorf("unsupported source: %q", s)
	}
	return src, nil
}

// String implements fmt.Stringer.
func (s Source) String() string { return string(s) }
