package httpclient

import (
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"quotes/internal/metrics"
)

// RateLimitSettings defines a proactive outbound HTTP rate limit for a single component.
// Zero RPS (or Burst) disables the limiter.
type RateLimitSettings struct {
	Component string
	RPS       float64
	Burst     int
}

// Enabled reports whether the limiter should be installed.
func (s RateLimitSettings) Enabled() bool {
	return s.RPS > 0 && s.Burst > 0
}

// WrapRateLimited wraps next with a token-bucket limiter. Stack order (outer first):
//
//	circuit breaker → rate limiter → retry → base
//
// The limiter sits outside retry so retries do not consume extra tokens per wallet-backend design.
func WrapRateLimited(next http.RoundTripper, s RateLimitSettings) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if !s.Enabled() {
		return next
	}
	return &rateLimitedTransport{
		next:      next,
		limiter:   rate.NewLimiter(rate.Limit(s.RPS), s.Burst),
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
