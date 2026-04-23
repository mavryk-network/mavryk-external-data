package httpclient

import (
	"net/http"
	"sync"
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

// WrapRateLimited wraps next with a token-bucket limiter. Stack order (outer first):
//
//	circuit breaker → rate limiter → retry → base
//
// The limiter sits outside retry so retries do not consume extra tokens (matches
// wallet-backend design). Per-component limiters are shared process-wide so that
// additional HTTP clients (new tokens, backfill workers) do not multiply the RPS
// actually sent to the upstream API.
func WrapRateLimited(next http.RoundTripper, s RateLimitSettings) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
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
