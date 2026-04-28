package httpclient

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"quotes/internal/metrics"
)

// RateLimitSettings defines a proactive outbound HTTP rate limit for a single component.
// RPS == 0 means "disabled". RPS > 0 with Burst <= 0 is auto-corrected by Normalized() to
// Burst = max(1, round(2*RPS)) — see refactoring_v2 §3.1.
type RateLimitSettings struct {
	Component string
	RPS       float64
	Burst     int
}

// Normalized returns a copy with Burst auto-corrected when only RPS is set.
// Idempotent under repeat calls.
func (s RateLimitSettings) Normalized() RateLimitSettings {
	if s.RPS > 0 && s.Burst <= 0 {
		s.Burst = int(s.RPS*2 + 0.5)
		if s.Burst < 1 {
			s.Burst = 1
		}
	}
	return s
}

// Enabled reports whether the limiter should be installed. Auto-normalizes first so
// that RPS=0.5, Burst=0 (a common config mistake) doesn't silently disable throttling.
func (s RateLimitSettings) Enabled() bool {
	n := s.Normalized()
	return n.RPS > 0 && n.Burst > 0
}

// sharedLimiters holds one token bucket per component so every *http.Client built for
// the same external service (e.g. "coingecko") shares the same budget.
// Without this the effective rate against the remote API scales with the number of
// clients (one per token × collector + backfill), making the configured RPS misleading.
var (
	sharedLimitersMu sync.Mutex
	sharedLimiters   = map[string]*rate.Limiter{}
)

func sharedLimiter(component string, rps float64, burst int) *rate.Limiter {
	// No component name → caller wants an isolated limiter (e.g. tests).
	if component == "" {
		return rate.NewLimiter(rate.Limit(rps), burst)
	}
	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	if l, ok := sharedLimiters[component]; ok {
		// If settings change at runtime (e.g. tests), apply the newest ones.
		if float64(l.Limit()) != rps {
			l.SetLimit(rate.Limit(rps))
		}
		if l.Burst() != burst {
			l.SetBurst(burst)
		}
		return l
	}
	l := rate.NewLimiter(rate.Limit(rps), burst)
	sharedLimiters[component] = l
	return l
}

// ResetSharedLimiters drops every entry from the shared limiter registry.
// Test helper. Production code never calls this.
func ResetSharedLimiters() {
	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	for k := range sharedLimiters {
		delete(sharedLimiters, k)
	}
}

// WrapRateLimited wraps next with a token-bucket limiter. Stack order (outer first):
//
//	circuit breaker → rate limiter → retry → base
//
// The limiter sits outside retry so retries do not consume extra tokens. Per-component
// limiters are shared process-wide so that additional HTTP clients (new tokens, backfill
// workers) do not multiply the RPS actually sent to the upstream API.
func WrapRateLimited(next http.RoundTripper, s RateLimitSettings) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	s = s.Normalized()
	if !s.Enabled() {
		return next
	}
	return &rateLimitedTransport{
		next:      next,
		limiter:   sharedLimiter(s.Component, s.RPS, s.Burst),
		component: s.Component,
	}
}

type rateLimitedTransport struct {
	next      http.RoundTripper
	limiter   *rate.Limiter
	component string
}

func (t *rateLimitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.limiter.Allow() {
		return t.next.RoundTrip(req)
	}

	start := time.Now()
	if err := t.limiter.Wait(req.Context()); err != nil {
		return nil, err
	}
	if t.component != "" {
		metrics.OutboundHTTPRateLimitWaitSeconds.WithLabelValues(t.component).Observe(time.Since(start).Seconds())
	}
	return t.next.RoundTrip(req)
}
