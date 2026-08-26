package prices

import (
	"fmt"
	"strings"
)

// Token is an FT token symbol (matches tokens.symbol in DB). Validated against the
// runtime registry; never construct directly. The registry is seeded at startup
// from the in-process bootstrap (which mirrors the `tokens` table) and can be
// extended via RegisterToken before validation runs.
type Token string

// TokenInfo carries the static metadata that never changes once a token is in the
// system: how upstream sources address it, how to format human numbers, etc.
type TokenInfo struct {
	Symbol   Token
	Name     string
	Decimals int
	// CoinGeckoID is the upstream coin ID (e.g. "mavryk-network"). Empty when the
	// token is not on CoinGecko.
	CoinGeckoID string
	Enabled     bool
}

var (
	tokenRegistry = map[Token]TokenInfo{}
)

// RegisterTokens replaces the registry with the supplied list. Called once at
// startup from the lookup-loader; idempotent under repeat calls.
//
// Symbols are normalized exactly like NewToken lowercases lookups — a
// mixed-case ops-inserted `tokens` row would otherwise be collected by jobs
// yet 404 on every API request.
func RegisterTokens(infos []TokenInfo) {
	next := make(map[Token]TokenInfo, len(infos))
	for _, info := range infos {
		info.Symbol = Token(strings.ToLower(strings.TrimSpace(string(info.Symbol))))
		next[info.Symbol] = info
	}
	tokenRegistry = next
}

// NewToken returns the canonical Token for s, or an error if unsupported.
func NewToken(s string) (Token, error) {
	t := Token(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := tokenRegistry[t]; !ok {
		return "", fmt.Errorf("unsupported token: %q", s)
	}
	return t, nil
}

// MustNewToken is the bootstrap-time variant for hard-coded callers (jobs).
// Panics if not registered — registration must happen first.
func MustNewToken(s string) Token {
	t, err := NewToken(s)
	if err != nil {
		panic(err)
	}
	return t
}

// LookupToken returns metadata for the token, ok=false when not registered.
func LookupToken(t Token) (TokenInfo, bool) {
	info, ok := tokenRegistry[t]
	return info, ok
}

// EnabledTokens returns only the tokens with enabled=true.
func EnabledTokens() []TokenInfo {
	out := make([]TokenInfo, 0, len(tokenRegistry))
	for _, info := range tokenRegistry {
		if info.Enabled {
			out = append(out, info)
		}
	}
	return out
}

// String implements fmt.Stringer.
func (t Token) String() string { return string(t) }
