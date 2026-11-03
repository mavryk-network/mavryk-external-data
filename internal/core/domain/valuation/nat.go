package valuation

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// ParseSupplyNat deliberately does not accept JSON floating point or exponents.
func ParseSupplyNat(raw json.RawMessage, maxDigits int) (string, error) {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
	} else {
		s = string(raw)
	}
	if len(s) == 0 || len(s) > maxDigits {
		return "", fmt.Errorf("invalid nat length")
	}
	for _, b := range []byte(s) {
		if b < '0' || b > '9' {
			return "", fmt.Errorf("invalid nat")
		}
	}
	n, _ := new(big.Int).SetString(s, 10)
	return n.String(), nil
}
func ValidSupplyNat(s string, n int) bool {
	canonical, err := ParseSupplyNat(json.RawMessage(s), n)
	return err == nil && canonical == s
}
func TrustedSupplyMetadata(source string) bool {
	return source == "dipdup" || source == "mvkt_inline" || strings.HasPrefix(source, "operator:") && len(source) > 9
}
