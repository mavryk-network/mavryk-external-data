package quotes

import "strings"

// Token represents a supported token
type Token string

const (
	TokenMVRK Token = "mvrk"
	TokenUSDT Token = "usdt"
)

var supportedTokens = map[Token]bool{
	TokenMVRK: true,
	TokenUSDT: true,
}

// IsTokenSupported checks if a token is supported
func IsTokenSupported(tokenName string) bool {
	return supportedTokens[Token(strings.ToLower(tokenName))]
}

// GetSupportedTokens returns a list of supported token names
func GetSupportedTokens() []Token {
	tokens := make([]Token, 0, len(supportedTokens))
	for token := range supportedTokens {
		tokens = append(tokens, token)
	}
	return tokens
}

// GetSupportedTokenNames returns a list of supported token names as strings
func GetSupportedTokenNames() []string {
	tokens := make([]string, 0, len(supportedTokens))
	for token := range supportedTokens {
		tokens = append(tokens, string(token))
	}
	return tokens
}

// GetCoinGeckoID returns the CoinGecko coin ID for a token
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
