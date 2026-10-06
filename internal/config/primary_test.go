package config

import "testing"

func TestPrimaryConfig_RequiresExplicitPaymentIdentityAndScale(t *testing.T) {
	good := PrimaryConfig{Enabled: true, Network: "basenet", IndexerURL: "https://basenet.api.mavryk.network/v1", Launchpads: []string{"KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN"}, PaymentTokens: []PrimaryPaymentToken{{Contract: "KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v", TokenID: "0", Decimals: primaryScale(6)}}}
	if err := (&Config{Primary: good}).validatePrimary(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"network", "payments", "decimals", "missing_decimals", "duplicate", "url", "contract"} {
		t.Run(name, func(t *testing.T) {
			cfg := good
			cfg.PaymentTokens = append([]PrimaryPaymentToken(nil), good.PaymentTokens...)
			switch name {
			case "network":
				cfg.Network = ""
			case "payments":
				cfg.PaymentTokens = nil
			case "decimals":
				cfg.PaymentTokens[0].Decimals = primaryScale(-1)
			case "missing_decimals":
				cfg.PaymentTokens[0].Decimals = nil
			case "duplicate":
				cfg.PaymentTokens = append(cfg.PaymentTokens, cfg.PaymentTokens[0])
			case "url":
				cfg.IndexerURL = "file:///tmp"
			case "contract":
				cfg.PaymentTokens[0].Contract = "KT1/escape"
			}
			if err := (&Config{Primary: cfg}).validatePrimary(); err == nil {
				t.Fatal("invalid historical configuration accepted")
			}
		})
	}
}

func TestPrimaryConfig_EnvironmentOverrides(t *testing.T) {
	t.Setenv("PRIMARY_VALUATION_ENABLED", "true")
	t.Setenv("PRIMARY_VALUATION_NETWORK", "basenet")
	t.Setenv("PRIMARY_VALUATION_INDEXER_URL", "https://basenet.api.mavryk.network/v1")
	t.Setenv("PRIMARY_VALUATION_LAUNCHPADS", "KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN")
	t.Setenv("PRIMARY_VALUATION_PAYMENT_TOKENS", `[{"contract":"KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v","token_id":"0","decimals":6}]`)
	var cfg Config
	if err := overridePrimary(&cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.validatePrimary(); err != nil {
		t.Fatal(err)
	}
	if cfg.Primary.PaymentTokens[0].Decimals == nil || *cfg.Primary.PaymentTokens[0].Decimals != 6 {
		t.Fatal("explicit scale lost")
	}
}

func primaryScale(n int) *int { return &n }
