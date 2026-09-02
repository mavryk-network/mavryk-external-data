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

// Config load must never refuse a dev-shaped setup: auth off, a well-known DB
// password and a local JWT verify key are all accepted in every gin mode,
// including release. Operational safety is the deploy's responsibility, not a
// startup gate.
func TestValidate_NoProductionSafetyRefusals(t *testing.T) {
	localPub := testLocalVerifyPublicKeyBase64(t)
	tests := []struct {
		name       string
		ginMode    string
		host       string
		authEnable *bool
		dbPassword string
		localKey   string
	}{
		{name: "release + auth disabled", ginMode: "release", authEnable: boolPtr(false), dbPassword: "s3cret-strong"},
		{name: "release + default db password", ginMode: "release", authEnable: boolPtr(true), dbPassword: "postgres"},
		{name: "release + makefile db password", ginMode: "release", authEnable: boolPtr(true), dbPassword: "qwerty"},
		{name: "release + local verify key", ginMode: "release", authEnable: boolPtr(true), dbPassword: "postgres", localKey: localPub},
		{name: "derived release (0.0.0.0) + auth disabled", host: "0.0.0.0", authEnable: boolPtr(false), dbPassword: "postgres"},
		{name: "derived debug (localhost) + auth disabled", host: "localhost", authEnable: boolPtr(false), dbPassword: "postgres"},
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
			// Unrelated to the removed guards: with auth on and no local key,
			// validateAuth still requires a JWKS base URL.
			c.Auth.MBIOJWTBaseURL = "https://mbio.test"
			c.Auth.JWTLocalVerifyPublicKeyBase64 = tc.localKey
			c.Database.Password = tc.dbPassword

			// The real entry point, not hand-picked validators: a re-added
			// guard would be a separate entry in the Validate() chain and must
			// fail this test.
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate refused a dev-shaped config: %v", err)
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
