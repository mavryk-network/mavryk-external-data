package config

import (
	"testing"
	"time"
)

// TestOverrideWithEnv_ServerTimeoutAndCacheTTL locks in that the two knobs
// documented in .env.example — SERVER_HANDLER_TIMEOUT and
// SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS — are actually read from the
// environment (they were documented but silently ignored before).
func TestOverrideWithEnv_ServerTimeoutAndCacheTTL(t *testing.T) {
	t.Setenv("SERVER_HANDLER_TIMEOUT", "7s")
	t.Setenv("SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS", "12")

	c := &Config{}
	if err := overrideWithEnv(c); err != nil {
		t.Fatalf("overrideWithEnv: %v", err)
	}

	if got := c.Server.HandlerTimeout.D(); got != 7*time.Second {
		t.Errorf("HandlerTimeout = %s, want 7s", got)
	}
	if got := c.Server.LatestQuoteCacheTTLSeconds; got != 12 {
		t.Errorf("LatestQuoteCacheTTLSeconds = %d, want 12", got)
	}
}

// TestOverrideWithEnv_InvalidValuesRejected ensures a malformed value surfaces
// a clear error instead of being silently dropped.
func TestOverrideWithEnv_InvalidValuesRejected(t *testing.T) {
	t.Run("bad duration", func(t *testing.T) {
		t.Setenv("SERVER_HANDLER_TIMEOUT", "notaduration")
		if err := overrideWithEnv(&Config{}); err == nil {
			t.Fatal("expected error for invalid SERVER_HANDLER_TIMEOUT, got nil")
		}
	})
	t.Run("bad int", func(t *testing.T) {
		t.Setenv("SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS", "abc")
		if err := overrideWithEnv(&Config{}); err == nil {
			t.Fatal("expected error for invalid SERVER_LATEST_QUOTE_CACHE_TTL_SECONDS, got nil")
		}
	})
}

func TestOverrideWithEnv_ServerRateLimitAndTrustedProxies(t *testing.T) {
	t.Setenv("SERVER_RATE_LIMIT_RPS", "12.5")
	t.Setenv("SERVER_RATE_LIMIT_BURST", "25")
	t.Setenv("SERVER_RATE_LIMIT_PER_IP", "false")
	t.Setenv("SERVER_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.1")

	cfg := &Config{}
	if err := overrideWithEnv(cfg); err != nil {
		t.Fatalf("overrideWithEnv: %v", err)
	}
	if cfg.Server.RateLimit.RPS != 12.5 {
		t.Errorf("rps = %v, want 12.5", cfg.Server.RateLimit.RPS)
	}
	if cfg.Server.RateLimit.Burst != 25 {
		t.Errorf("burst = %d, want 25", cfg.Server.RateLimit.Burst)
	}
	if cfg.Server.RateLimit.PerIP {
		t.Errorf("per_ip = true, want false")
	}
	if len(cfg.Server.TrustedProxies) != 2 ||
		cfg.Server.TrustedProxies[0] != "10.0.0.0/8" ||
		cfg.Server.TrustedProxies[1] != "192.168.1.1" {
		t.Errorf("trusted_proxies = %v", cfg.Server.TrustedProxies)
	}
}
