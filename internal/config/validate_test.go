package config

import (
	"strings"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

// TestValidateProductionSafety_AuthGuard locks in the release-mode fail-safe:
// disabling auth while gin_mode=release must refuse to start, so a production
// deploy can never silently serve RWA routes unauthenticated on the public
// listener. Non-release modes (local dev / CI) keep the AUTH_ENABLED=false
// convenience.
func TestValidateProductionSafety_AuthGuard(t *testing.T) {
	tests := []struct {
		name       string
		ginMode    string
		host       string
		authEnable *bool
		dbPassword string
		wantErr    bool
		errSubstr  string
	}{
		{
			name:       "release + auth disabled -> refuse",
			ginMode:    "release",
			authEnable: boolPtr(false),
			dbPassword: "s3cret-strong",
			wantErr:    true,
			errSubstr:  "auth is disabled",
		},
		{
			name:       "release + auth enabled (explicit) -> ok",
			ginMode:    "release",
			authEnable: boolPtr(true),
			dbPassword: "s3cret-strong",
			wantErr:    false,
		},
		{
			name:       "release + auth default (nil -> on) -> ok",
			ginMode:    "release",
			authEnable: nil,
			dbPassword: "s3cret-strong",
			wantErr:    false,
		},
		{
			name:       "release + auth enabled + default db password -> refuse on password",
			ginMode:    "release",
			authEnable: boolPtr(true),
			dbPassword: "postgres",
			wantErr:    true,
			errSubstr:  "database.password",
		},
		{
			name:       "debug + auth disabled -> ok (dev/CI convenience)",
			ginMode:    "debug",
			authEnable: boolPtr(false),
			dbPassword: "postgres",
			wantErr:    false,
		},
		{
			name:       "empty gin_mode + localhost host + auth disabled -> ok (derived debug)",
			ginMode:    "",
			host:       "localhost",
			authEnable: boolPtr(false),
			dbPassword: "postgres",
			wantErr:    false,
		},
		{
			name:       "empty gin_mode + 0.0.0.0 host + auth disabled -> refuse (derived release)",
			ginMode:    "",
			host:       "0.0.0.0",
			authEnable: boolPtr(false),
			dbPassword: "s3cret-strong",
			wantErr:    true,
			errSubstr:  "auth is disabled",
		},
		{
			name:       "empty gin_mode + 0.0.0.0 host + default db password -> refuse (derived release)",
			ginMode:    "",
			host:       "0.0.0.0",
			authEnable: nil,
			dbPassword: "postgres",
			wantErr:    true,
			errSubstr:  "database.password",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{}
			c.Server.GinMode = tc.ginMode
			c.Server.Host = tc.host
			c.Auth.Enabled = tc.authEnable
			c.Database.Password = tc.dbPassword

			err := c.validateProductionSafety()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
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
