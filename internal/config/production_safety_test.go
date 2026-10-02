package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Check the real env -> defaults -> validation path with an absent YAML
// password, matching the configuration shipped in the production image.
func TestLoad_ProductionPasswordRequired(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // Do not load the developer's .env file.
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  host: 0.0.0.0\nauth:\n  mbio_jwt_issuer: mbio-api-gateway\n  mbio_jwt_base_url: https://mbio.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVER_HOST", "0.0.0.0")
	t.Setenv("AUTH_ENABLED", "true")
	t.Setenv("AUTH_MBIO_JWT_ISSUER", "mbio-api-gateway")
	t.Setenv("AUTH_MBIO_JWT_BASE_URL", "https://mbio.test")
	t.Setenv("AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY", "")
	for _, tc := range []struct {
		name     string
		ginMode  string
		password string
		wantErr  string
	}{
		{name: "explicit release missing password", ginMode: "release", wantErr: "POSTGRES_PASSWORD"},
		{name: "derived release missing password", wantErr: "POSTGRES_PASSWORD"},
		{name: "derived release explicit default", password: "qwerty", wantErr: "well-known default"},
		{name: "derived release configured password", password: "unique-deployment-secret"},
		{name: "debug retains local fallback", ginMode: "debug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SERVER_GIN_MODE", tc.ginMode)
			t.Setenv("POSTGRES_PASSWORD", tc.password)
			cfg, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() = %v, want error containing %q", err, tc.wantErr)
				}
				if cfg != nil {
					t.Fatal("Load returned unsafe configuration alongside error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() = %v, want success", err)
			}
			wantPassword := tc.password
			if tc.ginMode == "debug" {
				wantPassword = "postgres"
			}
			if cfg.Database.Password != wantPassword {
				t.Fatal("Load did not preserve the configured password or local fallback")
			}
		})
	}
}
