package http

import (
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/testutil"

	"github.com/gin-gonic/gin"
)

// These tests drive the ASSEMBLED App — NewApp -> buildPublicEngine /
// buildInternalEngine -> SetupRoutes — through the real middleware chain, so
// deleting the RWA auth wrapper (or the rate limiter, or the /metrics gate)
// from the router turns them red. They reach the engines via the unexported
// App.publicServer / App.internalServer, which is why they live in-package.

const (
	appTestIssuer   = "https://mbio.test/app-issuer"
	appTestAudience = "mavryk-external-data"
	appTestSubject  = "mbio-api-gateway"
	appTestSymbol   = "DEMO-USD"
	appTestIntPort  = "19091"
)

type appTestEnv struct {
	app      *App
	public   *gin.Engine
	internal *gin.Engine // nil in single-port mode
	priv     *rsa.PrivateKey
}

// newAppTestEnv builds a real App. mutate customises the config after the
// local-key auth defaults are in place (gin_mode=test keeps config's
// release-mode refusal of local-key verification out of the picture).
//
// configureGinMode writes the process-global gin mode and gin.Recovery captures
// gin.DefaultErrorWriter at construction time, so both globals are pinned here.
// No test in this file may call t.Parallel().
func newAppTestEnv(t *testing.T, mutate func(*config.Config)) appTestEnv {
	t.Helper()
	appTestPinGlobals(t)

	priv, publicPEM, _, err := testutil.GenerateRSAJWTKeyPair(2048)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	cfg := &config.Config{}
	cfg.Server.GinMode = "test"
	cfg.Auth.MBIOJWTIssuer = appTestIssuer
	cfg.Auth.MBIOJWTAudience = appTestAudience
	cfg.Auth.JWTLocalVerifyPublicKeyBase64 = testutil.EncodePublicKeyBase64(publicPEM)
	if mutate != nil {
		mutate(cfg)
	}

	app, err := NewApp(AppDeps{Config: cfg})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	env := appTestEnv{app: app, priv: priv}
	env.public = app.publicServer.Handler.(*gin.Engine)
	if app.internalServer != nil {
		env.internal = app.internalServer.Handler.(*gin.Engine)
	}
	return env
}

func appTestPinGlobals(t *testing.T) {
	t.Helper()
	mode, errWriter := gin.Mode(), gin.DefaultErrorWriter
	gin.DefaultErrorWriter = io.Discard // nil handler deps make Recovery noisy
	t.Cleanup(func() {
		gin.SetMode(mode)
		gin.DefaultErrorWriter = errWriter
	})
}

// authHeader mints a Bearer header signed with the key this env's engine trusts.
// Issuer and audience default to the configured values; Subject is left to the
// caller so the missing-`sub` path can be minted.
func (e appTestEnv) authHeader(t *testing.T, opts testutil.LocalJWTOpts) string {
	t.Helper()
	if opts.Issuer == "" {
		opts.Issuer = appTestIssuer
	}
	if opts.Audience == "" {
		opts.Audience = appTestAudience
	}
	tok, err := testutil.SignLocalJWT(e.priv, opts)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + tok
}

func appTestGet(t *testing.T, h http.Handler, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func appTestGetForwardedFor(t *testing.T, h http.Handler, path, xff string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Forwarded-For", xff)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func appTestErrCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", body, err)
	}
	return env.Code
}

// appTestGuardedPaths derives the concrete request paths the RWA auth wrapper is
// meant to cover from the engine's own route table, so a newly added /v1/rwa/*
// route is exercised without editing this file.
func appTestGuardedPaths(t *testing.T, engine *gin.Engine) []string {
	t.Helper()
	var paths []string
	for _, ri := range engine.Routes() {
		if ri.Method != http.MethodGet {
			continue
		}
		if ri.Path == "/v1/rwa" || strings.HasPrefix(ri.Path, "/v1/rwa/") || ri.Path == "/v1/pairs/rwa" {
			paths = append(paths, strings.ReplaceAll(ri.Path, ":symbol", appTestSymbol))
		}
	}
	// Without this the callers would pass vacuously if the enumeration broke.
	for _, want := range []string{"/v1/rwa", "/v1/rwa/" + appTestSymbol, "/v1/pairs/rwa"} {
		if !appTestContains(paths, want) {
			t.Fatalf("route enumeration missed %s; got %v", want, paths)
		}
	}
	if len(paths) < 8 {
		t.Fatalf("expected at least 8 RWA-scoped routes, got %d: %v", len(paths), paths)
	}
	return paths
}

func appTestContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func appTestHasRoute(engine *gin.Engine, method, path string) bool {
	for _, ri := range engine.Routes() {
		if ri.Method == method && ri.Path == path {
			return true
		}
	}
	return false
}

func appTestCountPrefix(engine *gin.Engine, prefix string) int {
	n := 0
	for _, ri := range engine.Routes() {
		if strings.HasPrefix(ri.Path, prefix) {
			n++
		}
	}
	return n
}

func TestApp_PublicEngine_RWARoutesRejectMissingToken(t *testing.T) {
	env := newAppTestEnv(t, nil)
	for _, path := range appTestGuardedPaths(t, env.public) {
		w := appTestGet(t, env.public, path, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: status = %d, want 401 — is the RWA auth middleware still mounted?; body=%s",
				path, w.Code, w.Body.String())
			continue
		}
		if code := appTestErrCode(t, w.Body.Bytes()); code != "UNAUTHORIZED" {
			t.Errorf("GET %s: error code = %q, want UNAUTHORIZED", path, code)
		}
	}
}

func TestApp_PublicEngine_OpenRoutesNeedNoToken(t *testing.T) {
	env := newAppTestEnv(t, nil)
	for _, path := range []string{"/healthz", "/openapi.yaml", "/docs", "/docs/"} {
		w := appTestGet(t, env.public, path, "")
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200; body=%s", path, w.Code, w.Body.String())
		}
		if w.Body.Len() == 0 {
			t.Errorf("GET %s: empty body", path)
		}
	}
}

func TestApp_PublicEngine_ValidTokenCrossesAuthBoundary(t *testing.T) {
	env := newAppTestEnv(t, nil)
	auth := env.authHeader(t, testutil.LocalJWTOpts{Subject: appTestSubject, TTL: 5 * time.Minute})

	for _, path := range appTestGuardedPaths(t, env.public) {
		w := appTestGet(t, env.public, path, auth)
		// Handler deps are nil in this App, so routes that reach their handler may
		// 500 through gin.Recovery. Any status other than 401/403 proves the auth
		// middleware handed the request on.
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("GET %s with a valid token: status = %d, want the request past auth; body=%s",
				path, w.Code, w.Body.String())
		}
	}

	// /ohlcv is the one guarded route with no dependencies: a 501 shows the
	// request reached its handler rather than merely a recovered panic.
	w := appTestGet(t, env.public, "/v1/rwa/"+appTestSymbol+"/ohlcv", auth)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("GET /v1/rwa/%s/ohlcv with a valid token: status = %d, want 501; body=%s",
			appTestSymbol, w.Code, w.Body.String())
	}
}

func TestApp_PublicEngine_JWTFailureEnvelopes(t *testing.T) {
	env := newAppTestEnv(t, nil)
	tests := []struct {
		name       string
		opts       testutil.LocalJWTOpts
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing subject",
			opts:       testutil.LocalJWTOpts{TTL: 5 * time.Minute},
			wantStatus: http.StatusForbidden,
			wantCode:   "FORBIDDEN",
		},
		{
			name: "expired",
			opts: testutil.LocalJWTOpts{
				Subject:  appTestSubject,
				IssuedAt: time.Now().UTC().Add(-2 * time.Hour),
				TTL:      time.Minute,
			},
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UNAUTHORIZED",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := appTestGet(t, env.public, "/v1/pairs/rwa", env.authHeader(t, tt.opts))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
			if code := appTestErrCode(t, w.Body.Bytes()); code != tt.wantCode {
				t.Errorf("error code = %q, want %q", code, tt.wantCode)
			}
		})
	}
}

func TestApp_InternalEngine_ServesRWAWithoutToken(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) { cfg.Server.InternalPort = appTestIntPort })
	if env.internal == nil {
		t.Fatal("internalServer is nil with server.internal_port set")
	}
	if !strings.HasSuffix(env.app.internalServer.Addr, ":"+appTestIntPort) {
		t.Errorf("internal Addr = %q, want it to bind :%s", env.app.internalServer.Addr, appTestIntPort)
	}

	for _, path := range appTestGuardedPaths(t, env.internal) {
		w := appTestGet(t, env.internal, path, "")
		if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
			t.Errorf("internal GET %s: status = %d, want no auth wrapper on the internal listener; body=%s",
				path, w.Code, w.Body.String())
		}
	}
	if w := appTestGet(t, env.internal, "/v1/rwa/"+appTestSymbol+"/ohlcv", ""); w.Code != http.StatusNotImplemented {
		t.Errorf("internal GET /v1/rwa/%s/ohlcv: status = %d, want 501", appTestSymbol, w.Code)
	}

	// Same App, same route graph — the public half must still be guarded.
	if w := appTestGet(t, env.public, "/v1/pairs/rwa", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("public GET /v1/pairs/rwa: status = %d, want 401", w.Code)
	}
}

func TestApp_RateLimit_ThroughAssembledChain(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) {
		cfg.Server.InternalPort = appTestIntPort
		cfg.Server.RateLimit = config.ServerRateLimitConfig{RPS: 1, Burst: 2}
	})

	for i := 1; i <= 3; i++ {
		w := appTestGet(t, env.public, "/openapi.yaml", "")
		want := http.StatusOK
		if i == 3 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("public request %d: status = %d, want %d", i, w.Code, want)
		}
		if want == http.StatusTooManyRequests {
			if code := appTestErrCode(t, w.Body.Bytes()); code != "RATE_LIMITED" {
				t.Errorf("public request %d: error code = %q, want RATE_LIMITED", i, code)
			}
		}
	}

	for i := 1; i <= 10; i++ {
		if w := appTestGet(t, env.internal, "/openapi.yaml", ""); w.Code != http.StatusOK {
			t.Fatalf("internal request %d: status = %d, want 200 — the internal engine must not rate-limit", i, w.Code)
		}
	}
}

// Probes are exempt from the limiter. With per_ip=false every anonymous caller
// shares one global bucket, so without the exemption a traffic burst would push
// the kubelet's liveness probe into 429 and restart the pod.
func TestApp_RateLimit_ExemptsHealthProbes(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) {
		cfg.Server.RateLimit = config.ServerRateLimitConfig{RPS: 1, Burst: 2}
	})
	// Drain the bucket on a normal route first.
	for i := 0; i < 5; i++ {
		appTestGet(t, env.public, "/openapi.yaml", "")
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		for i := 1; i <= 10; i++ {
			if got := appTestGet(t, env.public, path, "").Code; got == http.StatusTooManyRequests {
				t.Fatalf("%s request %d: 429 — probes must never be rate-limited", path, i)
			}
		}
	}
}

// SetTrustedProxies(nil) means ClientIP() is the direct peer, so a client cannot
// mint a fresh per-IP bucket by rotating X-Forwarded-For.
func TestApp_PerIPRateLimit_IgnoresClientSuppliedXFF(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) {
		cfg.Server.RateLimit = config.ServerRateLimitConfig{RPS: 1, Burst: 1, PerIP: true}
	})
	if w := appTestGetForwardedFor(t, env.public, "/openapi.yaml", "203.0.113.10"); w.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", w.Code)
	}
	w := appTestGetForwardedFor(t, env.public, "/openapi.yaml", "198.51.100.20")
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request from a different X-Forwarded-For: status = %d, want 429 — spoofed XFF must not open a fresh bucket", w.Code)
	}
}

func TestApp_MetricsExposure(t *testing.T) {
	t.Run("single port outside release serves metrics publicly", func(t *testing.T) {
		env := newAppTestEnv(t, nil)
		if env.internal != nil {
			t.Fatal("internalServer should be nil without server.internal_port")
		}
		w := appTestGet(t, env.public, "/metrics", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET /metrics: status = %d, want 200", w.Code)
		}
		if !strings.Contains(w.Body.String(), "# HELP") {
			t.Errorf("GET /metrics: body is not a Prometheus exposition: %q", w.Body.String())
		}
	})

	t.Run("single port in release withholds metrics from the public engine", func(t *testing.T) {
		env := newAppTestEnv(t, func(cfg *config.Config) { cfg.Server.GinMode = "release" })
		if gin.Mode() != gin.ReleaseMode {
			t.Fatalf("gin.Mode() = %q, want release", gin.Mode())
		}
		if appTestHasRoute(env.public, http.MethodGet, "/metrics") {
			t.Error("/metrics is registered on the public engine in single-port release mode")
		}
		if w := appTestGet(t, env.public, "/metrics", ""); w.Code != http.StatusNotFound {
			t.Errorf("GET /metrics: status = %d, want 404", w.Code)
		}
	})

	t.Run("internal port moves metrics onto the internal engine", func(t *testing.T) {
		env := newAppTestEnv(t, func(cfg *config.Config) { cfg.Server.InternalPort = appTestIntPort })
		if appTestHasRoute(env.public, http.MethodGet, "/metrics") {
			t.Error("/metrics is registered on the public engine while an internal port is configured")
		}
		if w := appTestGet(t, env.public, "/metrics", ""); w.Code != http.StatusNotFound {
			t.Errorf("public GET /metrics: status = %d, want 404", w.Code)
		}
		if w := appTestGet(t, env.internal, "/metrics", ""); w.Code != http.StatusOK {
			t.Errorf("internal GET /metrics: status = %d, want 200", w.Code)
		}
	})
}

// pprof discloses stack traces and lets a caller pin a CPU for 30s per profile
// request, and /debug/pprof sits outside the JWT-guarded /v1/rwa group — so it
// must never reach the internet-facing engine.
func TestApp_PprofEnabled_MountsOnInternalEngineOnly(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) {
		cfg.Server.PprofEnabled = true
		cfg.Server.InternalPort = appTestIntPort
	})
	if n := appTestCountPrefix(env.public, "/debug/pprof"); n != 0 {
		t.Errorf("public engine has %d /debug/pprof routes; pprof must be internal-only", n)
	}
	if n := appTestCountPrefix(env.internal, "/debug/pprof"); n == 0 {
		t.Error("pprof is enabled with an internal port but no /debug/pprof route is mounted on the internal engine")
	}
}

func TestApp_PprofDisabledByDefault(t *testing.T) {
	env := newAppTestEnv(t, func(cfg *config.Config) { cfg.Server.InternalPort = appTestIntPort })
	if n := appTestCountPrefix(env.public, "/debug/pprof"); n != 0 {
		t.Errorf("public engine exposes %d /debug/pprof routes with pprof_enabled unset", n)
	}
	if n := appTestCountPrefix(env.internal, "/debug/pprof"); n != 0 {
		t.Errorf("internal engine exposes %d /debug/pprof routes with pprof_enabled unset", n)
	}
}

func TestApp_AuthDisabled_LeavesPublicRWAOpen(t *testing.T) {
	disabled := false
	env := newAppTestEnv(t, func(cfg *config.Config) { cfg.Auth.Enabled = &disabled })
	w := appTestGet(t, env.public, "/v1/rwa/"+appTestSymbol+"/ohlcv", "")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 — auth.enabled=false must drop the wrapper; body=%s", w.Code, w.Body.String())
	}
}

func TestNewApp_InvalidLocalVerifyKey_ReturnsError(t *testing.T) {
	appTestPinGlobals(t)
	cfg := &config.Config{}
	cfg.Server.GinMode = "test"
	cfg.Auth.MBIOJWTIssuer = appTestIssuer
	cfg.Auth.JWTLocalVerifyPublicKeyBase64 = "bm90LXBlbQ==" // base64("not-pem")

	app, err := NewApp(AppDeps{Config: cfg})
	if err == nil {
		t.Fatal("NewApp succeeded with an unparseable local verify key, want error")
	}
	if app != nil {
		t.Error("NewApp returned a non-nil App alongside the error")
	}
}
