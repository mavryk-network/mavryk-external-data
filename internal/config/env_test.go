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
