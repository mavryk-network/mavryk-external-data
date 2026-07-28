package config

import (
	"strings"
	"testing"
)

func TestValidateAuth_RejectsHTTPJWKS(t *testing.T) {
	base := func(url string) *Config {
		c := &Config{}
		c.Auth.Enabled = boolPtr(true)
		c.Auth.MBIOJWTIssuer = "mbio-api-gateway"
		c.Auth.MBIOJWTBaseURL = url
		return c
	}

	if err := base("http://core-api.example.io").validateAuth(); err == nil {
		t.Error("http:// JWKS base must be rejected (MITM key injection)")
	}
	if err := base("https://core-api.example.io").validateAuth(); err != nil {
		t.Errorf("https:// JWKS base must be accepted, got %v", err)
	}
	if err := base("http://localhost:8080").validateAuth(); err != nil {
		t.Errorf("http://localhost must be allowed for dev, got %v", err)
	}
}

func TestValidateEquiteez_IndexerURLParsesWhenPasswordSet(t *testing.T) {
	c := &Config{}
	// no password → no constraint
	if err := c.validateEquiteez(); err != nil {
		t.Fatalf("no password should pass: %v", err)
	}

	c.Equiteez.IndexerPassword = "secret"
	c.Equiteez.IndexerURL = ""
	if err := c.validateEquiteez(); err == nil {
		t.Error("password set + empty URL must fail")
	}

	c.Equiteez.IndexerURL = "https://basenet.api.equiteez.com/v1/graphql"
	if err := c.validateEquiteez(); err != nil {
		t.Errorf("password set + valid URL must pass, got %v", err)
	}

	c.Equiteez.IndexerURL = "://missing-scheme"
	if err := c.validateEquiteez(); err == nil {
		t.Error("password set + unparseable URL must fail")
	}
}

func TestSetDefaults_OutboundMaxResponseBytesAndSSL(t *testing.T) {
	c := &Config{}
	setDefaults(c)
	if c.API.OutboundMaxResponseBytes != 16<<20 {
		t.Errorf("OutboundMaxResponseBytes default = %d, want %d", c.API.OutboundMaxResponseBytes, 16<<20)
	}
	if c.Database.SSLMode != "prefer" {
		t.Errorf("SSLMode default = %q, want prefer", c.Database.SSLMode)
	}
	// A negative value is the explicit "disabled" escape hatch — setDefaults must
	// not clobber it.
	c2 := &Config{}
	c2.API.OutboundMaxResponseBytes = -1
	setDefaults(c2)
	if c2.API.OutboundMaxResponseBytes != -1 {
		t.Errorf("negative cap must be preserved, got %d", c2.API.OutboundMaxResponseBytes)
	}
}

func TestValidateAuth_ErrorMentionsHTTPS(t *testing.T) {
	c := &Config{}
	c.Auth.Enabled = boolPtr(true)
	c.Auth.MBIOJWTIssuer = "mbio-api-gateway"
	c.Auth.MBIOJWTBaseURL = "http://evil.example.io"
	err := c.validateAuth()
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("expected an https-related error, got %v", err)
	}
}

func TestRWAPairSyncInterval_DefaultAndValidation(t *testing.T) {
	// Discovery latency is bounded by this knob; it must default to something
	// sane when RWA is on, and stay 0 (job no-op) when RWA is off.
	on := &Config{}
	on.RWA.Enabled = true
	setDefaults(on)
	if on.RWA.PairSyncIntervalSeconds != 3600 {
		t.Errorf("enabled: PairSyncIntervalSeconds = %d, want 3600", on.RWA.PairSyncIntervalSeconds)
	}

	off := &Config{}
	setDefaults(off)
	if off.RWA.PairSyncIntervalSeconds != 0 {
		t.Errorf("disabled: PairSyncIntervalSeconds = %d, want 0", off.RWA.PairSyncIntervalSeconds)
	}

	// Explicit operator value survives defaulting.
	custom := &Config{}
	custom.RWA.Enabled = true
	custom.RWA.PairSyncIntervalSeconds = 300
	setDefaults(custom)
	if custom.RWA.PairSyncIntervalSeconds != 300 {
		t.Errorf("explicit value clobbered: got %d, want 300", custom.RWA.PairSyncIntervalSeconds)
	}

	bad := &Config{}
	bad.RWA.PairSyncIntervalSeconds = -1
	if err := bad.validateRWA(); err == nil {
		t.Error("negative pair_sync_interval_seconds must be rejected")
	}
}

func TestOverrideWithEnv_RWAPairSyncInterval(t *testing.T) {
	t.Setenv("RWA_PAIR_SYNC_INTERVAL_SECONDS", "120")
	c := &Config{}
	if err := overrideWithEnv(c); err != nil {
		t.Fatalf("overrideWithEnv: %v", err)
	}
	if c.RWA.PairSyncIntervalSeconds != 120 {
		t.Errorf("PairSyncIntervalSeconds = %d, want 120", c.RWA.PairSyncIntervalSeconds)
	}

	t.Setenv("RWA_PAIR_SYNC_INTERVAL_SECONDS", "abc")
	if err := overrideWithEnv(&Config{}); err == nil {
		t.Error("malformed RWA_PAIR_SYNC_INTERVAL_SECONDS must error, not be silently ignored")
	}
}
