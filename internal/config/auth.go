package config

import "time"

// AuthConfig holds MBIO API Gateway JWT verification settings (RS256 via JWKS).
// The service never issues tokens; it only verifies Bearer tokens from the gateway.
//
// Verification is mounted on the **public** HTTP listener for RWA-scoped routes
// (/v1/rwa/* and /v1/pairs/rwa). The **internal** listener never wraps these
// routes in auth — intra-cluster callers reach them without a token.
//
// Local / e2e: set JWTLocalVerifyPublicKeyBase64 (base64 of PEM) + MBIOJWTIssuer
// to verify RS256 JWTs signed with the matching private key, skipping JWKS fetch.
type AuthConfig struct {
	// Enabled controls whether the MBIO JWT middleware is constructed at all.
	// Nil = default on (verify). When *false the public listener still serves
	// the RWA routes but without any auth wrapper — used in local dev and tests
	// where issuing real JWTs is too much friction.
	Enabled *bool `yaml:"enabled"`

	// MBIOAPIGatewayBaseURL — API Gateway base URL. Used as a fallback when
	// MBIOJWTBaseURL is unset (some deployments serve JWKS from the same host
	// that fronts the API).
	MBIOAPIGatewayBaseURL string `yaml:"mbio_api_gateway_base_url"`

	// MBIOJWTBaseURL — JWT / JWKS host base; JWKS is loaded from
	// {base}/.well-known/jwks.json. Takes precedence over MBIOAPIGatewayBaseURL.
	MBIOJWTBaseURL string `yaml:"mbio_jwt_base_url"`

	// MBIOJWTIssuer — expected `iss` claim; passed to jwt.WithIssuer. Required
	// when JWT verification is enabled.
	MBIOJWTIssuer string `yaml:"mbio_jwt_issuer"`

	// MBIOJWTAudience — expected `aud` claim; passed to jwt.WithAudience.
	// Required in this service (unlike rwa-backend, which warns and continues
	// when unset). Without it any MBIO-issued token for a sibling service would
	// be accepted on RWA data — see Validate.
	MBIOJWTAudience string `yaml:"mbio_jwt_audience"`

	// JWKSCacheTTL — how long the keyfunc cache holds JWKS before refresh
	// (default 5m).
	JWKSCacheTTL time.Duration `yaml:"jwks_cache_ttl"`

	// JWTLocalVerifyPublicKeyBase64 — standard base64 of an RSA public-key PEM,
	// for local-only RS256 verification without a JWKS endpoint. When non-empty
	// it short-circuits the JWKS path entirely (useful for tests and offline
	// docker-compose runs).
	JWTLocalVerifyPublicKeyBase64 string `yaml:"jwt_local_verify_public_key"`
}

// JWTVerificationEnabled reports whether the MBIO JWT middleware should be built.
// Default true; explicit `auth.enabled: false` (or AUTH_ENABLED=false) disables it.
func (a *AuthConfig) JWTVerificationEnabled() bool {
	if a == nil || a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

func defaultAuthConfig() AuthConfig {
	return AuthConfig{
		JWKSCacheTTL: 5 * time.Minute,
	}
}
