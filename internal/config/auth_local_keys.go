package config

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// LocalJWTVerifyConfigured reports whether local RSA public-key verification is
// selected — when true, the middleware skips the JWKS fetch entirely and uses
// the embedded key for signature checks. Intended for local dev and CI.
func (a *AuthConfig) LocalJWTVerifyConfigured() bool {
	if a == nil {
		return false
	}
	return strings.TrimSpace(a.JWTLocalVerifyPublicKeyBase64) != ""
}

// LocalJWTVerifyPublicKeyPEMBytes decodes JWTLocalVerifyPublicKeyBase64
// (standard base64 of a PEM-encoded RSA public key) and returns PEM bytes.
// Empty config returns (nil, nil).
func (a *AuthConfig) LocalJWTVerifyPublicKeyPEMBytes() ([]byte, error) {
	if a == nil {
		return nil, nil
	}
	b64 := strings.TrimSpace(strings.ReplaceAll(a.JWTLocalVerifyPublicKeyBase64, "\n", ""))
	if b64 == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("jwt_local_verify_public_key base64: %w", err)
	}
	return []byte(strings.TrimSpace(string(raw))), nil
}
