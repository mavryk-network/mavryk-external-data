package middleware_test

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quotes/internal/config"
	httpmw "quotes/internal/core/api/http/middleware"
	"quotes/internal/testutil"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	testIssuer   = "https://mbio.test/issuer"
	testAudience = "mavryk-external-data"
	testSubject  = "mbio-api-gateway"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// buildEngineWithLocalKey wires up MBIOJWT with a fresh RSA key pair and a
// single protected route. Returns the matching private key so tests can mint
// tokens against it.
func buildEngineWithLocalKey(t *testing.T) (*gin.Engine, *rsa.PrivateKey) {
	t.Helper()
	priv, publicPEM, _, err := testutil.GenerateRSAJWTKeyPair(2048)
	require.NoError(t, err)

	cfg := &config.AuthConfig{
		MBIOJWTIssuer:                 testIssuer,
		MBIOJWTAudience:               testAudience,
		JWTLocalVerifyPublicKeyBase64: testutil.EncodePublicKeyBase64(publicPEM),
	}
	logger := zerolog.Nop()
	mid, err := httpmw.MBIOJWT(cfg, &logger)
	require.NoError(t, err)

	r := gin.New()
	r.GET("/protected", mid, func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r, priv
}

func performGet(t *testing.T, r http.Handler, header string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func parseErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Code
}

func TestMBIOJWT_MissingHeader_Returns401(t *testing.T) {
	r, _ := buildEngineWithLocalKey(t)
	w := performGet(t, r, "")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "UNAUTHORIZED", parseErrCode(t, w.Body.Bytes()))
}

func TestMBIOJWT_NonBearerScheme_Returns401(t *testing.T) {
	r, _ := buildEngineWithLocalKey(t)
	w := performGet(t, r, "Basic dXNlcjpwYXNz")
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, "UNAUTHORIZED", parseErrCode(t, w.Body.Bytes()))
}

func TestMBIOJWT_EmptyBearer_Returns401(t *testing.T) {
	r, _ := buildEngineWithLocalKey(t)
	w := performGet(t, r, "Bearer ")
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMBIOJWT_ValidToken_PassesThrough(t *testing.T) {
	r, priv := buildEngineWithLocalKey(t)
	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:   testIssuer,
		Audience: testAudience,
		Subject:  testSubject,
		TTL:      5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "ok", w.Body.String())
}

func TestMBIOJWT_ExpiredToken_Returns401(t *testing.T) {
	r, priv := buildEngineWithLocalKey(t)
	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:   testIssuer,
		Audience: testAudience,
		Subject:  testSubject,
		IssuedAt: time.Now().UTC().Add(-2 * time.Hour),
		TTL:      time.Minute, // expired 119 minutes ago
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMBIOJWT_WrongIssuer_Returns401(t *testing.T) {
	r, priv := buildEngineWithLocalKey(t)
	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:   "https://wrong.example/iss",
		Audience: testAudience,
		Subject:  testSubject,
		TTL:      5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMBIOJWT_WrongAudience_Returns401(t *testing.T) {
	r, priv := buildEngineWithLocalKey(t)
	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:   testIssuer,
		Audience: "some-other-service",
		Subject:  testSubject,
		TTL:      5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMBIOJWT_NoAudienceConfigured_AcceptsTokenWithoutAud(t *testing.T) {
	// MBIO mints tokens without `aud`. When AuthConfig.MBIOJWTAudience is empty,
	// the middleware must skip WithAudience and accept the token. A startup
	// warn-log fires (covered by inspection, not assertion here).
	priv, publicPEM, _, err := testutil.GenerateRSAJWTKeyPair(2048)
	require.NoError(t, err)
	cfg := &config.AuthConfig{
		MBIOJWTIssuer: testIssuer,
		// MBIOJWTAudience intentionally empty.
		JWTLocalVerifyPublicKeyBase64: testutil.EncodePublicKeyBase64(publicPEM),
	}
	logger := zerolog.Nop()
	mid, err := httpmw.MBIOJWT(cfg, &logger)
	require.NoError(t, err)
	r := gin.New()
	r.GET("/protected", mid, func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:  testIssuer,
		Subject: testSubject,
		TTL:     5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestMBIOJWT_MissingSubject_Returns403(t *testing.T) {
	r, priv := buildEngineWithLocalKey(t)
	tok, err := testutil.SignLocalJWT(priv, testutil.LocalJWTOpts{
		Issuer:   testIssuer,
		Audience: testAudience,
		// Subject intentionally empty.
		TTL: 5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Equal(t, "FORBIDDEN", parseErrCode(t, w.Body.Bytes()))
}

func TestMBIOJWT_WrongSigningKey_Returns401(t *testing.T) {
	// Tampering one byte of a base64-encoded signature is unreliable (some flips
	// still yield a valid byte-string the verifier rejects only stochastically).
	// Sign with a completely different RSA key instead — that always fails.
	r, _ := buildEngineWithLocalKey(t)
	otherPriv, _, _, err := testutil.GenerateRSAJWTKeyPair(2048)
	require.NoError(t, err)
	tok, err := testutil.SignLocalJWT(otherPriv, testutil.LocalJWTOpts{
		Issuer:   testIssuer,
		Audience: testAudience,
		Subject:  testSubject,
		TTL:      5 * time.Minute,
	})
	require.NoError(t, err)
	w := performGet(t, r, "Bearer "+tok)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMBIOJWT_BuildFails_OnInvalidLocalPEM(t *testing.T) {
	cfg := &config.AuthConfig{
		MBIOJWTIssuer:                 testIssuer,
		MBIOJWTAudience:               testAudience,
		JWTLocalVerifyPublicKeyBase64: "bm90LXBlbQ==", // base64("not-pem")
	}
	logger := zerolog.Nop()
	_, err := httpmw.MBIOJWT(cfg, &logger)
	require.Error(t, err)
}
