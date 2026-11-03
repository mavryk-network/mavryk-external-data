package config

import (
	"fmt"
	"quotes/internal/core/domain/valuation"
	"time"
)

// Enabled is deliberately opt-in: deploy schema/API, then start observation.
type RWASupplyConfig struct {
	Enabled                  bool   `yaml:"enabled"`
	ExpectedChainID          string `yaml:"expected_chain_id"`
	IntervalSeconds          int    `yaml:"interval_seconds"`
	FreshSeconds             int    `yaml:"fresh_seconds"`
	IndexerLagSeconds        int    `yaml:"indexer_lag_seconds"`
	PriceFreshSeconds        int    `yaml:"price_fresh_seconds"`
	PrimaryPriceFreshSeconds int    `yaml:"primary_price_fresh_seconds"`
	RetentionSeconds         int    `yaml:"retention_seconds"`
	TickTimeoutSeconds       int    `yaml:"tick_timeout_seconds"`
	TokenTimeoutSeconds      int    `yaml:"token_timeout_seconds"`
	Concurrency              int    `yaml:"concurrency"`
	BlockLag                 int    `yaml:"block_lag"`
	PageSize                 int    `yaml:"page_size"`
	MaxLaunches              int    `yaml:"max_launches"`
	MaxResponseBytes         int    `yaml:"max_response_bytes"`
}

func (c RWASupplyConfig) WithDefaults(network string) RWASupplyConfig {
	for _, d := range []struct {
		p *int
		v int
	}{{&c.IntervalSeconds, 60}, {&c.FreshSeconds, 900}, {&c.IndexerLagSeconds, 120}, {&c.PriceFreshSeconds, 900}, {&c.PrimaryPriceFreshSeconds, 4500}, {&c.RetentionSeconds, 86400}, {&c.TickTimeoutSeconds, 50}, {&c.TokenTimeoutSeconds, 20}, {&c.Concurrency, 3}, {&c.BlockLag, 2}, {&c.PageSize, 100}, {&c.MaxLaunches, 1000}, {&c.MaxResponseBytes, 16 << 20}} {
		if *d.p == 0 {
			*d.p = d.v
		}
	}
	if c.ExpectedChainID == "" && network == "basenet" {
		c.ExpectedChainID = "NetXmtMsNf69w1w"
	}
	return c
}

func (c *Config) validateRWASupply() error {
	s := c.RWASupply.WithDefaults(c.RWA.PricingNetwork())
	for _, v := range []int{s.IntervalSeconds, s.FreshSeconds, s.IndexerLagSeconds, s.PriceFreshSeconds, s.PrimaryPriceFreshSeconds, s.RetentionSeconds, s.TickTimeoutSeconds, s.TokenTimeoutSeconds, s.Concurrency, s.BlockLag, s.PageSize, s.MaxLaunches, s.MaxResponseBytes} {
		if v <= 0 {
			return fmt.Errorf("rwa_supply: limits must be positive")
		}
	}
	if s.Enabled && (s.ExpectedChainID == "" || c.Equiteez.AllowlistURL == "") {
		return fmt.Errorf("rwa_supply requires expected_chain_id and CRM allowlist URL")
	}
	if s.FreshSeconds < 2*s.IntervalSeconds || s.RetentionSeconds <= s.FreshSeconds || s.RetentionSeconds <= s.PrimaryPriceFreshSeconds || s.RetentionSeconds <= s.PriceFreshSeconds || s.TokenTimeoutSeconds >= s.TickTimeoutSeconds || s.TickTimeoutSeconds >= s.IntervalSeconds || s.PageSize > 1000 || s.MaxResponseBytes > 16<<20 || s.Concurrency > 32 {
		return fmt.Errorf("rwa_supply: inconsistent time budgets or limits")
	}
	return nil
}
func (c RWASupplyConfig) Policy() valuation.Policy {
	return valuation.Policy{Fresh: time.Duration(c.FreshSeconds) * time.Second, PriceFresh: time.Duration(c.PriceFreshSeconds) * time.Second, PrimaryPriceFresh: time.Duration(c.PrimaryPriceFreshSeconds) * time.Second, Retention: time.Duration(c.RetentionSeconds) * time.Second}.Defaulted()
}
