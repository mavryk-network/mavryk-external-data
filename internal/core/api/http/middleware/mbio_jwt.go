package middleware

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"quotes/internal/config"
	httpCommon "quotes/internal/core/api/http/common"
	coreerrors "quotes/internal/core/common/errors"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
)

// MBIOJWT builds a Gin middleware that verifies RS256 Bearer JWTs issued by the
// MBIO Identity Provider and forwarded by the MBIO API Gateway.
//
// Verification path:
//   - JWKS over HTTP (default): keyfunc fetches and caches {base}/.well-known/jwks.json
//     with refresh interval = cfg.JWKSCacheTTL.
//   - Local RSA public key (when cfg.LocalJWTVerifyConfigured is true): the key
//     is read from cfg.JWTLocalVerifyPublicKeyBase64 (base64 of PEM) — used for
//     local dev / CI where contacting the real MBIO is impractical.
//
// Claim checks: iss (mandatory), exp/nbf (jwt/v5 default), sub (must be present).
// Audience is enforced only when cfg.MBIOJWTAudience is set; MBIO currently mints
// tokens without `aud`, so the default is "no aud check". A startup warn-log
// fires once when aud is empty so the missing check stays visible in ops logs.
//
// On any failure the middleware writes a JSON error envelope via httpCommon.RespondError
// and aborts the gin chain. No fallthrough — the wrapped handler never runs.
func MBIOJWT(cfg *config.AuthConfig, logger *zerolog.Logger) (gin.HandlerFunc, error) {
	log := logger.With().Str("component", "mbio_jwt").Logger()

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(cfg.MBIOJWTIssuer),
	}
	if aud := strings.TrimSpace(cfg.MBIOJWTAudience); aud != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(aud))
		log.Debug().Str("aud", aud).Msg("jwt_audience_check_enabled")
	} else {
		log.Warn().Msg("jwt_audience_check_disabled_set_AUTH_MBIO_JWT_AUDIENCE_to_enforce_aud")
	}

	keyFunc, err := buildKeyFunc(cfg, &log)
	if err != nil {
		return nil, err
	}

	return func(c *gin.Context) {
		tokenString, err := extractBearer(c.GetHeader("Authorization"))
		if err != nil {
			httpCommon.RespondError(c, err)
			c.Abort()
			return
		}
		if err := requireJWTHeaderTyp(tokenString); err != nil {
			httpCommon.RespondError(c, coreerrors.InvalidArgument(err.Error()))
			c.Abort()
			return
		}

		// We only consume RegisteredClaims (RFC 7519). The token's `sub` is the
		// MBIO Gateway service identity; this middleware's whole job is to
		// verify signature + issuer + audience + expiry. Per-user authorization
		// is not performed here (this service serves cross-tenant market data).
		var claims jwt.RegisteredClaims
		token, err := jwt.ParseWithClaims(tokenString, &claims, keyFunc, parserOpts...)
		if err != nil {
			respondJWTErr(c, err, &log)
			c.Abort()
			return
		}
		if !token.Valid {
			httpCommon.RespondError(c, coreerrors.Unauthorized("Invalid token"))
			c.Abort()
			return
		}
		if strings.TrimSpace(claims.Subject) == "" {
			httpCommon.RespondError(c, coreerrors.Forbidden("JWT missing subject"))
			c.Abort()
			return
		}
		c.Next()
	}, nil
}

func buildKeyFunc(cfg *config.AuthConfig, log *zerolog.Logger) (jwt.Keyfunc, error) {
	if cfg.LocalJWTVerifyConfigured() {
		pemBytes, err := cfg.LocalJWTVerifyPublicKeyPEMBytes()
		if err != nil {
			return nil, err
		}
		pub, err := jwt.ParseRSAPublicKeyFromPEM(pemBytes)
		if err != nil {
			return nil, fmt.Errorf("local jwt verify public key: %w", err)
		}
		if bits := pub.N.BitLen(); bits < 2048 {
			return nil, fmt.Errorf("local jwt verify public key is %d-bit RSA; refusing keys below 2048 bits", bits)
		}
		// Warn, not Debug: this swaps the JWKS trust anchor for a static key,
		// which must be loudly visible if it ever reaches a deployed env.
		log.Warn().Msg("jwt_verify_mode=local_rsa_public_key_jwks_bypassed")
		return func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return pub, nil
		}, nil
	}

	base := strings.TrimRight(strings.TrimSpace(cfg.MBIOJWTBaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(cfg.MBIOAPIGatewayBaseURL), "/")
	}
	jwksURL := base + "/.well-known/jwks.json"
	override := keyfunc.Override{
		RefreshInterval: cfg.JWKSCacheTTL,
		HTTPTimeout:     15 * time.Second,
	}
	kf, err := keyfunc.NewDefaultOverrideCtx(context.Background(), []string{jwksURL}, override)
	if err != nil {
		return nil, fmt.Errorf("jwks init for %s: %w", jwksURL, err)
	}
	log.Debug().Str("jwks_url", jwksURL).Msg("jwt_verify_mode=jwks")
	return kf.Keyfunc, nil
}

func extractBearer(authHeader string) (string, error) {
	if authHeader == "" {
		return "", coreerrors.Unauthorized("Authorization header required")
	}
	parts := strings.SplitN(authHeader, " ", 3)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return "", coreerrors.Unauthorized("Invalid authorization format. Expected 'Bearer <token>'")
	}
	tok := strings.TrimSpace(parts[1])
	if tok == "" {
		return "", coreerrors.Unauthorized("Empty bearer token")
	}
	return tok, nil
}

func respondJWTErr(c *gin.Context, err error, log *zerolog.Logger) {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		httpCommon.RespondError(c, coreerrors.Unauthorized("Token expired"))
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		httpCommon.RespondError(c, coreerrors.Unauthorized("Invalid token issuer"))
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		httpCommon.RespondError(c, coreerrors.Unauthorized("Invalid token audience"))
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		// jwt/v5 raises this when WithAudience is set but `aud` is absent on the token.
		httpCommon.RespondError(c, coreerrors.Unauthorized("Token missing required claim"))
	case errors.Is(err, jwt.ErrTokenInvalidClaims):
		httpCommon.RespondError(c, coreerrors.Forbidden("Invalid JWT claims"))
	default:
		log.Debug().Err(err).Msg("jwt_verify_failed")
		httpCommon.RespondError(c, coreerrors.Unauthorized("Invalid token"))
	}
}

type jwtHeader struct {
	Typ string `json:"typ"`
}

// requireJWTHeaderTyp rejects tokens whose header `typ` is set to something other
// than `JWT` (case-insensitive). When `typ` is absent we accept — jwt/v5 already
// validates the signing algorithm via WithValidMethods.
func requireJWTHeaderTyp(token string) error {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil
	}
	var h jwtHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil
	}
	if h.Typ != "" && !strings.EqualFold(h.Typ, "JWT") {
		return fmt.Errorf("jwt header typ must be JWT")
	}
	return nil
}
