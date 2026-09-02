package httpclient

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"quotes/internal/metrics"
)

// RateLimitSettings defines a proactive outbound HTTP rate limit for one
// component. RPS == 0 disables it; Burst <= 0 is auto-corrected by Normalized().
type RateLimitSettings struct {
	Component string
	RPS       float64
	Burst     int
}

// Normalized returns a copy with Burst auto-corrected when only RPS is set.
func (s RateLimitSettings) Normalized() RateLimitSettings {
	if s.RPS > 0 && s.Burst <= 0 {
		s.Burst = int(s.RPS*2 + 0.5)
		if s.Burst < 1 {
			s.Burst = 1
		}
	}
	return s
}

// Enabled reports whether the limiter should be installed, normalizing first so
// RPS=0.5, Burst=0 doesn't silently disable throttling.
func (s RateLimitSettings) Enabled() bool {
	n := s.Normalized()
	return n.RPS > 0 && n.Burst > 0
}

// sharedLimiters holds one token bucket per component, so every *http.Client for
// the same service shares one budget instead of multiplying the effective rate.
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

// lookupSharedLimiter returns the process-wide limiter for a component, or nil
// when rate limiting is off; retryTransport charges a token per retry through it.
func lookupSharedLimiter(component string) *rate.Limiter {
	if component == "" {
		return nil
	}
	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	return sharedLimiters[component]
}

// ResetSharedLimiters drops every entry from the registry. Test helper.
func ResetSharedLimiters() {
	sharedLimitersMu.Lock()
	defer sharedLimitersMu.Unlock()
	for k := range sharedLimiters {
		delete(sharedLimiters, k)
	}
}

// WrapRateLimited wraps next with a token-bucket limiter as the OUTERMOST layer:
//
//	rate limiter → logging → circuit breaker → retry → counter → base
//
// Retries charge their own token inside retryTransport, which surfaces the
// upstream outcome rather than a throttling error so the breaker never sees it.
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
