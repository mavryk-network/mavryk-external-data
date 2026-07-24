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
