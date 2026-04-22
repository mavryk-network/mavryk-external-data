package quotes

import (
	"fmt"
	"strings"
)

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

// tokenTableSuffix maps each supported token to the PostgreSQL relation name under schema mev.
// Only these literals may be used in qualified table names — never concatenate user input.
var tokenTableSuffix = map[Token]string{
	TokenMVRK: "mvrk",
	TokenUSDT: "usdt",
}

// QuoteHypertableQualifiedName returns the qualified table name (mev.<suffix>) for quote storage.
// It is the only supported way to resolve dynamic table names from a token string.
func QuoteHypertableQualifiedName(tokenName string) (string, error) {
	t := Token(strings.ToLower(strings.TrimSpace(tokenName)))
	if !supportedTokens[t] {
		return "", fmt.Errorf("token '%s' is not supported", tokenName)
	}
	suffix, ok := tokenTableSuffix[t]
	if !ok || suffix == "" {
		return "", fmt.Errorf("token '%s' is not supported", tokenName)
	}
	return "mev." + suffix, nil
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
