package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Payment scales are operator-verified metadata for an exact contract/token ID.
// Unknown identities never inherit another USDT generation's decimals.
type PrimaryPaymentToken struct {
	Contract string `yaml:"contract" json:"contract"`
	TokenID  string `yaml:"token_id" json:"token_id"`
	Decimals *int   `yaml:"decimals" json:"decimals"`
}

type PrimaryConfig struct {
	Enabled       bool                  `yaml:"enabled"`
	Network       string                `yaml:"network"`
	IndexerURL    string                `yaml:"indexer_url"`
	Launchpads    []string              `yaml:"launchpads"`
	PaymentTokens []PrimaryPaymentToken `yaml:"payment_tokens"`
}

func primaryContract(s string) bool {
	return len(s) == 36 && strings.HasPrefix(s, "KT1") && !strings.ContainsAny(s, "/:? \t\n")
}

func (c *Config) validatePrimary() error {
	if !c.Primary.Enabled {
		return nil
	}
	u, err := url.Parse(c.Primary.IndexerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return fmt.Errorf("primary.indexer_url must be an HTTP(S) API base URL")
	}
	if c.Primary.Network == "" || len(c.Primary.Launchpads) == 0 || len(c.Primary.Launchpads) > 4 {
		return fmt.Errorf("primary requires a network and 1-4 launchpads")
	}
	for _, address := range c.Primary.Launchpads {
		if !primaryContract(address) {
			return fmt.Errorf("invalid primary launchpad")
		}
	}
	seen := map[string]bool{}
	for _, token := range c.Primary.PaymentTokens {
		_, err := strconv.ParseUint(token.TokenID, 10, 64)
		key := token.Contract + ":" + token.TokenID
		if !primaryContract(token.Contract) || err != nil || token.Decimals == nil || *token.Decimals < 0 || *token.Decimals > 40 || seen[key] {
			return fmt.Errorf("primary.payment_tokens requires unique identities and confirmed decimals")
		}
		seen[key] = true
	}
	if len(seen) == 0 {
		return fmt.Errorf("primary.payment_tokens is required")
	}
	return nil
}

func overridePrimary(c *Config) error {
	if v := os.Getenv("PRIMARY_VALUATION_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("PRIMARY_VALUATION_ENABLED: %w", err)
		}
		c.Primary.Enabled = b
	}
	if v := os.Getenv("PRIMARY_VALUATION_NETWORK"); v != "" {
		c.Primary.Network = strings.ToLower(strings.TrimSpace(v))
	}
	if v := os.Getenv("PRIMARY_VALUATION_INDEXER_URL"); v != "" {
		c.Primary.IndexerURL = strings.TrimRight(v, "/")
	}
	if v := os.Getenv("PRIMARY_VALUATION_LAUNCHPADS"); v != "" {
		c.Primary.Launchpads = nil
		for _, address := range strings.Split(v, ",") {
			c.Primary.Launchpads = append(c.Primary.Launchpads, strings.TrimSpace(address))
		}
	}
	if v := os.Getenv("PRIMARY_VALUATION_PAYMENT_TOKENS"); v != "" {
		if err := json.Unmarshal([]byte(v), &c.Primary.PaymentTokens); err != nil {
			return fmt.Errorf("PRIMARY_VALUATION_PAYMENT_TOKENS: %w", err)
		}
	}
	return nil
}
