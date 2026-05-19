// Package testutil holds shared helpers for unit and integration tests.
//
// local_jwt.go provides RSA key generation and RS256 token signing so middleware
// tests can mint valid Bearer JWTs without contacting a real MBIO Identity
// Provider. Mirrors the layout used in mavryk-rwa-backend.
package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GenerateRSAJWTKeyPair creates an RSA key pair and returns the private key
// plus PKIX public PEM and PKCS#1 private PEM. Use bits >= 2048; smaller values
// are silently bumped to 2048.
func GenerateRSAJWTKeyPair(bits int) (priv *rsa.PrivateKey, publicPEM []byte, privatePEM []byte, err error) {
	if bits < 2048 {
		bits = 2048
	}
	priv, err = rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, nil, nil, err
	}
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, nil, nil, err
	}
	publicPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return priv, publicPEM, privatePEM, nil
}

// EncodePublicKeyBase64 wraps a PEM-encoded public key in standard base64,
// matching the format AuthConfig.JWTLocalVerifyPublicKeyBase64 expects.
func EncodePublicKeyBase64(publicPEM []byte) string {
	return base64.StdEncoding.EncodeToString(publicPEM)
}

// LocalJWTOpts controls non-default token fields. Zero values fall back to a
// well-formed token with sensible defaults.
type LocalJWTOpts struct {
	Issuer    string
	Audience  string
	Subject   string
	TTL       time.Duration
	NotBefore time.Time     // optional; default = now
	IssuedAt  time.Time     // optional; default = now
	ExpiresIn time.Duration // alias for TTL when TTL == 0
	OmitTyp   bool          // skip the typ=JWT header (test the typ-check path)
}

// SignLocalJWT builds an RS256 JWT with typ=JWT (unless OmitTyp). Returns the
// compact serialization (header.payload.signature).
func SignLocalJWT(priv *rsa.PrivateKey, opts LocalJWTOpts) (string, error) {
	if priv == nil {
		return "", fmt.Errorf("private key is nil")
	}
	if strings.TrimSpace(opts.Issuer) == "" {
		return "", fmt.Errorf("issuer is required")
	}
	// Subject is intentionally optional: middleware tests need to mint a token
	// with an empty `sub` to exercise the "JWT missing subject" path.
	now := time.Now().UTC()
	iat := opts.IssuedAt
	if iat.IsZero() {
		iat = now
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = opts.ExpiresIn
	}
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	claims := jwt.RegisteredClaims{
		Issuer:    opts.Issuer,
		Subject:   opts.Subject,
		IssuedAt:  jwt.NewNumericDate(iat),
		ExpiresAt: jwt.NewNumericDate(iat.Add(ttl)),
	}
	if !opts.NotBefore.IsZero() {
		claims.NotBefore = jwt.NewNumericDate(opts.NotBefore)
	}
	if a := strings.TrimSpace(opts.Audience); a != "" {
		claims.Audience = jwt.ClaimStrings{a}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if !opts.OmitTyp {
		tok.Header["typ"] = "JWT"
	}
	return tok.SignedString(priv)
}
