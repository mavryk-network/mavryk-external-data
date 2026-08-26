package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"quotes/internal/config"

	"github.com/gin-gonic/gin"
)

func newRateLimitedEngine(cfg config.ServerRateLimitConfig) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimit(cfg))
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/healthz", ok)
	r.GET("/readyz", ok)
	r.GET("/v1/thing", ok)
	return r
}

func get(r *gin.Engine, path string) int {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code
}

func TestRateLimit_DisabledWhenZeroRPS(t *testing.T) {
	r := newRateLimitedEngine(config.ServerRateLimitConfig{RPS: 0})
	for i := 0; i < 50; i++ {
		if code := get(r, "/v1/thing"); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}
}

func TestRateLimit_Returns429PastBurst(t *testing.T) {
	r := newRateLimitedEngine(config.ServerRateLimitConfig{RPS: 1, Burst: 2})
	if code := get(r, "/v1/thing"); code != http.StatusOK {
		t.Fatalf("first = %d, want 200", code)
	}
	if code := get(r, "/v1/thing"); code != http.StatusOK {
		t.Fatalf("second = %d, want 200", code)
	}
	if code := get(r, "/v1/thing"); code != http.StatusTooManyRequests {
		t.Fatalf("third = %d, want 429", code)
	}
}

func TestRateLimit_HealthRoutesExempt(t *testing.T) {
	for _, perIP := range []bool{false, true} {
		r := newRateLimitedEngine(config.ServerRateLimitConfig{RPS: 1, Burst: 1, PerIP: perIP})
		// Exhaust the bucket, then hammer the probes: they must never 429.
		get(r, "/v1/thing")
		get(r, "/v1/thing")
		for i := 0; i < 20; i++ {
			if code := get(r, "/healthz"); code != http.StatusOK {
				t.Fatalf("perIP=%v healthz %d = %d, want 200", perIP, i, code)
			}
			if code := get(r, "/readyz"); code != http.StatusOK {
				t.Fatalf("perIP=%v readyz %d = %d, want 200", perIP, i, code)
			}
		}
	}
}
