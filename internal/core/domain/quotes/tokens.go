package quotes

import "strings"

// Token represents a supported token identifier (stored as the `token` column in `quotes`).
type Token string

const (
	TokenMVRK Token = "mvrk"
	TokenUSDT Token = "usdt"
)

var supportedTokens = map[Token]bool{
	TokenMVRK: true,
	TokenUSDT: true,
}

// NormalizeToken returns the canonical Token for name, or ("", false) if not supported.
// All storage / API layers must use this to validate user input before querying.
func NormalizeToken(name string) (Token, bool) {
	t := Token(strings.ToLower(strings.TrimSpace(name)))
	if !supportedTokens[t] {
		return "", false
	}
	return t, true
}

// IsTokenSupported checks if a token is supported.
func IsTokenSupported(name string) bool {
	_, ok := NormalizeToken(name)
	return ok
}

// GetSupportedTokens returns a list of supported tokens.
func GetSupportedTokens() []Token {
	tokens := make([]Token, 0, len(supportedTokens))
	for token := range supportedTokens {
		tokens = append(tokens, token)
	}
	return tokens
}

// GetSupportedTokenNames returns a list of supported token names as strings.
func GetSupportedTokenNames() []string {
	tokens := make([]string, 0, len(supportedTokens))
	for token := range supportedTokens {
		tokens = append(tokens, string(token))
	}
	return tokens
}

// GetCoinGeckoID returns the CoinGecko coin ID for a token.
func GetCoinGeckoID(token Token) string {
	switch token {
	case TokenMVRK:
		return "mavryk-network"
	case TokenUSDT:
		return "tether"
	default:
		return ""
	}
}
