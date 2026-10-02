package config

import (
	"strings"
	"testing"
	"time"

	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/testutil"
)

func boolPtr(b bool) *bool { return &b }

// testLocalVerifyPublicKeyBase64 mints the base64-PEM form validateAuth expects
// for AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY.
func testLocalVerifyPublicKeyBase64(t *testing.T) string {
	t.Helper()
	_, publicPEM, _, err := testutil.GenerateRSAJWTKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	return testutil.EncodePublicKeyBase64(publicPEM)
}

// Exercise the full validation chain so safety checks cannot silently become
// disconnected from startup. Derived modes must enforce the same policy.
func TestValidate_ProductionSafety(t *testing.T) {
	localPub := testLocalVerifyPublicKeyBase64(t)
	tests := []struct {
		name       string
		ginMode    string
		host       string
		authEnable *bool
		dbPassword string
		localKey   string
		wantError  string
	}{
		{name: "release + auth disabled", ginMode: "release", authEnable: boolPtr(false), dbPassword: "s3cret-strong", wantError: "auth is disabled"},
		{name: "release + default db password", ginMode: "release", dbPassword: "postgres", wantError: "well-known default"},
		{name: "release + admin db password", ginMode: "release", dbPassword: "admin", wantError: "well-known default"},
		{name: "release + password db password", ginMode: "release", dbPassword: "password", wantError: "well-known default"},
		{name: "release + changeme db password", ginMode: "release", dbPassword: "changeme", wantError: "well-known default"},
		{name: "release + makefile db password", ginMode: "release", dbPassword: "qwerty", wantError: "well-known default"},
		{name: "release + normalized default db password", ginMode: " ReLeAsE ", dbPassword: " PoStGrEs ", wantError: "well-known default"},
		{name: "release + missing password", ginMode: "release", wantError: "POSTGRES_PASSWORD"},
		{name: "release + blank password", ginMode: "release", dbPassword: " \t", wantError: "POSTGRES_PASSWORD"},
		{name: "release + local verify key", ginMode: "release", dbPassword: "s3cret-strong", localKey: localPub, wantError: "dev/CI-only"},
		{name: "derived release + auth disabled", host: "0.0.0.0", authEnable: boolPtr(false), dbPassword: "s3cret-strong", wantError: "auth is disabled"},
		{name: "derived release + default password", host: "0.0.0.0", dbPassword: "postgres", wantError: "well-known default"},
		{name: "derived release + local key", host: "0.0.0.0", dbPassword: "s3cret-strong", localKey: localPub, wantError: "dev/CI-only"},
		{name: "release + valid JWKS config", ginMode: "release", dbPassword: "s3cret-strong"},
		{name: "derived release + valid JWKS config", host: "0.0.0.0", dbPassword: "s3cret-strong"},
		{name: "debug + auth disabled", ginMode: "debug", authEnable: boolPtr(false), dbPassword: "postgres"},
		{name: "test + auth disabled", ginMode: "test", authEnable: boolPtr(false), dbPassword: "postgres"},
		{name: "debug + local verify key", ginMode: "debug", dbPassword: "postgres", localKey: localPub},
		{name: "test + local verify key", ginMode: "test", dbPassword: "postgres", localKey: localPub},
		{name: "derived debug (localhost) + auth disabled", host: "localhost", authEnable: boolPtr(false), dbPassword: "postgres"},
		{name: "derived debug (127.0.0.1) + local key", host: "127.0.0.1", dbPassword: "postgres", localKey: localPub},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			setDefaults(c)
			c.Server.GinMode = tc.ginMode
			if tc.host != "" {
				c.Server.Host = tc.host
			}
			c.Server.Port = "3010"
			c.Auth.Enabled = tc.authEnable
			c.Auth.MBIOJWTIssuer = "https://mbio.test/issuer"
			c.Auth.MBIOJWTBaseURL = "https://mbio.test"
			c.Auth.JWTLocalVerifyPublicKeyBase64 = tc.localKey
			c.Database.Password = tc.dbPassword

			err := c.Validate()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantError)
				}
				if tc.dbPassword != "" && tc.dbPassword != "password" && strings.Contains(err.Error(), tc.dbPassword) {
					t.Fatalf("validation error exposed database password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() = %v, want success", err)
			}
		})
	}
}

// TestValidateFXMaxStaleness pins the relationship between the two staleness
// thresholds. The soft budget only tags `fx.stale`; the fixed hard cap refuses
// the conversion outright. A budget at or above the cap inverts them — every
// rate the budget meant to serve flagged stale gets refused first, so the flag
// becomes unreachable and the knob silently does nothing.
func TestValidateFXMaxStaleness(t *testing.T) {
	hardCap := int(apiprices.FXHardStalenessCap / time.Second)
	tests := []struct {
		name      string
		seconds   int
		wantErr   bool
		errSubstr string
	}{
		{name: "default (0 -> in-code 300s)", seconds: 0},
		{name: "well below the cap", seconds: 300},
		{name: "just below the cap", seconds: hardCap - 1},
		{name: "at the cap leaves no room for fx.stale", seconds: hardCap, wantErr: true, errSubstr: "hard staleness cap"},
		{name: "milliseconds typo", seconds: 300000, wantErr: true, errSubstr: "hard staleness cap"},
		{name: "negative", seconds: -1, wantErr: true, errSubstr: "must be >= 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			setDefaults(c)
			c.Server.Host = "localhost"
			c.Server.Port = "3010"
			c.Server.FXMaxStalenessSeconds = tc.seconds

			err := c.validateServer()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
