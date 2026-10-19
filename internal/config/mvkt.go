package config

import (
	"fmt"
	"net/url"
)

const DefaultMvKTIndexerURL = "https://basenet.api.mavryk.network/v1"

// MvKTConfig is the shared connection used for on-chain reads.
type MvKTConfig struct {
	IndexerURL string `yaml:"indexer_url"`
}

func (c *Config) validateMvKT() error {
	u, err := url.Parse(c.MvKT.IndexerURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("mvkt.indexer_url must be an HTTP(S) API base URL")
	}
	return nil
}
