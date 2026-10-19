package equiteez

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ContractAllowlist is the complete CRM document. Nil arrays are rejected:
// missing fields are not equivalent to an intentionally empty list.
type ContractAllowlist struct {
	SchemaVersion int      `json:"schema_version"`
	Orderbooks    []string `json:"orderbooks"`
	BaseTokens    []string `json:"base_tokens"`
	QuoteTokens   []string `json:"quote_tokens"`
	Launchpads    []string `json:"launchpads"`
}

func ParseAllowlist(body []byte) (ContractAllowlist, error) {
	var a ContractAllowlist
	if err := json.Unmarshal(body, &a); err != nil {
		return a, fmt.Errorf("decode CRM allowlist: %w", err)
	}
	if a.SchemaVersion != 1 {
		return a, fmt.Errorf("unsupported CRM allowlist schema_version: %d", a.SchemaVersion)
	}
	for name, values := range map[string][]string{"orderbooks": a.Orderbooks, "base_tokens": a.BaseTokens, "quote_tokens": a.QuoteTokens, "launchpads": a.Launchpads} {
		if values == nil {
			return a, fmt.Errorf("CRM allowlist is missing %s array", name)
		}
		seen := make(map[string]bool)
		for _, addr := range values {
			if len(addr) != 36 || !strings.HasPrefix(addr, "KT1") || strings.TrimSpace(addr) != addr || seen[addr] {
				return a, fmt.Errorf("invalid or duplicate contract in CRM %s", name)
			}
			for _, char := range addr[3:] {
				if !strings.ContainsRune("123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz", char) {
					return a, fmt.Errorf("invalid contract in CRM %s", name)
				}
			}
			seen[addr] = true
		}
	}
	return a, nil
}

func (c *Client) GetContractAllowlist(ctx context.Context, endpoint string) (ContractAllowlist, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ContractAllowlist{}, fmt.Errorf("invalid CRM allowlist URL")
	}
	resp, err := c.allowlistHTTP.Do(req)
	if err != nil {
		return ContractAllowlist{}, fmt.Errorf("CRM allowlist request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ContractAllowlist{}, fmt.Errorf("CRM allowlist HTTP %d", resp.StatusCode)
	}
	const maxBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return ContractAllowlist{}, err
	}
	if len(body) > maxBytes {
		return ContractAllowlist{}, fmt.Errorf("CRM allowlist exceeds size limit")
	}
	return ParseAllowlist(body)
}

func containsAddress(addresses []string, address string) bool {
	for _, a := range addresses {
		if a == address {
			return true
		}
	}
	return false
}

func (a ContractAllowlist) PairAllowed(token, quote, book string) bool {
	return containsAddress(a.BaseTokens, token) && containsAddress(a.QuoteTokens, quote) && containsAddress(a.Orderbooks, book)
}

func (a ContractAllowlist) LaunchAllowed(token, quote, launchpad string) bool {
	return containsAddress(a.BaseTokens, token) && containsAddress(a.QuoteTokens, quote) && containsAddress(a.Launchpads, launchpad)
}
