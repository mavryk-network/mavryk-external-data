package config

import (
	"fmt"
	"net/url"
	"time"
)

func (c EquiteezConfig) ValidateRecovery() error {
	u, err := url.Parse(c.AllowlistURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("rwa.enabled=true requires a valid equiteez.allowlist_url (CONTRACT_ALLOWLIST_URL)")
	}
	if c.HeadMaxAgeSeconds < 0 {
		return fmt.Errorf("equiteez.head_max_age_seconds must be >= 0")
	}
	return nil
}

func (c EquiteezConfig) HeadMaxAge() time.Duration {
	if c.HeadMaxAgeSeconds > 0 {
		return time.Duration(c.HeadMaxAgeSeconds) * time.Second
	}
	return 5 * time.Minute
}

func (c EquiteezConfig) ExpectedIndexes() []string {
	if len(c.RequiredIndexes) > 0 {
		return c.RequiredIndexes
	}
	return []string{"token_index", "orderbook_index", "quote_token_index", "super_admin_index", "kyc_index", "launchpad_index"}
}
