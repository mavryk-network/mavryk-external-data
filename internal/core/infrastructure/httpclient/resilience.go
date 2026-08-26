package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"

	"quotes/internal/metrics"
)

// ResilienceSettings configures exponential-backoff retry and an optional circuit breaker
// for outbound HTTP (applied as a RoundTripper chain).
type ResilienceSettings struct {
	Component string

	RetryMaxAttempts int
	RetryInitialWait time.Duration
	RetryMaxWait     time.Duration

	CircuitBreakerDisabled         bool
	CBInterval                     time.Duration
	CBOpenTimeout                  time.Duration
	CBHalfOpenMaxRequests          uint32
	CBTripAfterConsecutiveFailures uint32
}

// Normalized returns a copy with defaults applied.
func (s ResilienceSettings) Normalized() ResilienceSettings {
	out := s
	if out.RetryMaxAttempts <= 0 {
		out.RetryMaxAttempts = 3
	}
	if out.RetryInitialWait <= 0 {
		out.RetryInitialWait = 200 * time.Millisecond
	}
	if out.RetryMaxWait <= 0 {
		out.RetryMaxWait = 5 * time.Second
	}
	if !out.CircuitBreakerDisabled {
		if out.CBInterval <= 0 {
			out.CBInterval = 60 * time.Second
		}
		if out.CBOpenTimeout <= 0 {
			out.CBOpenTimeout = 30 * time.Second
		}
		if out.CBHalfOpenMaxRequests == 0 {
			out.CBHalfOpenMaxRequests = 1
		}
	}
	return out
}

// WrapResilientTransport returns base wrapped with retry (counter inside).
// Order: retry → counter → base.
// The counter sits inside retry so each attempt (including retries) increments
// outbound_http_requests_total — `total - outbound_http_retries_total` then
// gives logical-request count.
//
// The circuit breaker is applied separately via WrapCircuitBreaker so clients
// can place it OUTSIDE the rate limiter: an open breaker then fast-fails
// before a limiter token is consumed (and before the caller blocks in Wait).
func WrapResilientTransport(base http.RoundTripper, s ResilienceSettings) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	s = s.Normalized()
	counted := WrapCounted(base, s.Component)
	if s.RetryMaxAttempts == 1 {
		return counted
	}
	return &retryTransport{next: counted, s: s}
}

// WrapCircuitBreaker wraps rt with the component's shared circuit breaker
// (no-op when disabled). Intended as the OUTERMOST layer of the client stack.
func WrapCircuitBreaker(rt http.RoundTripper, s ResilienceSettings) http.RoundTripper {
	s = s.Normalized()
	if s.CircuitBreakerDisabled {
		return rt
	}
	return newCircuitBreakerRoundTripper(s, rt)
}

func shouldRetryHTTPStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Deterministic/permanent failures: retrying only adds latency and pads
	// outbound_http_retries_total while a misconfiguration persists.
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		msg := urlErr.Err.Error()
		if strings.Contains(msg, "unsupported protocol scheme") || strings.Contains(msg, "no Host in request URL") {
			return false
		}
	}
	return true
}

func waitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiterWait blocks until the shared limiter grants a token (no-op when lim is
// nil, i.e. rate limiting disabled for the component). Records the wait so the
// retry-side throttling is visible in the same metric as the request-side wait.
func limiterWait(ctx context.Context, lim *rate.Limiter, comp string) error {
	if lim == nil {
		return nil
	}
	start := time.Now()
	if err := lim.Wait(ctx); err != nil {
		return err
	}
	if comp != "" {
		metrics.OutboundHTTPRateLimitWaitSeconds.WithLabelValues(comp).Observe(time.Since(start).Seconds())
	}
	return nil
}

// retryAfter parses a Retry-After response header (RFC 7231): either delta-seconds
// or an HTTP-date. Returns 0 when absent, malformed, or in the past.
func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

type retryTransport struct {
	next http.RoundTripper
	s    ResilienceSettings
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.next == nil {
		t.next = http.DefaultTransport
	}
	s := t.s.Normalized()
	comp := s.Component
	attempts := s.RetryMaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	// The outer WrapRateLimited charges one token for the logical request
	// (attempt 1). Retries happen below it and would otherwise bypass the
	// limiter — turning one call into RetryMaxAttempts unthrottled network hits
	// exactly when the upstream is returning 429. Charge a token before each
	// RETRY so total attempts never exceed the configured RPS. nil = limiter
	// disabled for this component.
	lim := lookupSharedLimiter(comp)

	var snapshot []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		snapshot, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = s.RetryInitialWait
	bo.MaxInterval = s.RetryMaxWait
	bo.Multiplier = 2
	bo.RandomizationFactor = 0.25
	bo.MaxElapsedTime = 0
	bo.Reset()

	var resp *http.Response
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		r2 := req.Clone(req.Context())
		if snapshot != nil {
			b := snapshot
			r2.Body = io.NopCloser(bytes.NewReader(b))
			r2.ContentLength = int64(len(b))
			r2.GetBody = func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(b)), nil
			}
		}
		resp, err = t.next.RoundTrip(r2)
		if err != nil {
			if attempt >= attempts || !retryableErr(err) {
				return nil, err
			}
			if comp != "" {
				metrics.OutboundHTTPRetriesTotal.WithLabelValues(comp).Inc()
			}
			if werr := waitContext(req.Context(), bo.NextBackOff()); werr != nil {
				return nil, werr
			}
			if werr := limiterWait(req.Context(), lim, comp); werr != nil {
				return nil, werr
			}
			continue
		}
		if shouldRetryHTTPStatus(resp.StatusCode) {
			if attempt >= attempts {
				return resp, nil
			}
			// Honor Retry-After (delta-seconds or HTTP-date) on 429/503 instead
			// of blind exponential backoff; fall back to backoff when absent.
			wait := bo.NextBackOff()
			if ra := retryAfter(resp); ra > 0 {
				wait = ra
			}
			_ = resp.Body.Close()
			if comp != "" {
				metrics.OutboundHTTPRetriesTotal.WithLabelValues(comp).Inc()
			}
			if werr := waitContext(req.Context(), wait); werr != nil {
				return nil, werr
			}
			if werr := limiterWait(req.Context(), lim, comp); werr != nil {
				return nil, werr
			}
			continue
		}
		return resp, nil
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

type circuitBreakerRoundTripper struct {
	cb   *gobreaker.CircuitBreaker
	next http.RoundTripper
}

// sharedBreakers holds one circuit breaker per component (the "CB registry key"
// promised in config/api.go), mirroring sharedLimiters. Without it every client
// instance carried its own breaker: N tokens produced 2N+1 CoinGecko breakers
// (each needing its own failure streak to trip), the hourly pair-sync rebuilt
// its client — and breaker — every tick so it could never trip at all, and
// every construction reset the shared state gauge to "closed" mid-incident.
// First construction per component wins; settings are uniform per upstream.
var (
	sharedBreakersMu sync.Mutex
	sharedBreakers   = map[string]*gobreaker.CircuitBreaker{}
)

func sharedBreaker(s ResilienceSettings) *gobreaker.CircuitBreaker {
	if s.Component == "" {
		return newBreaker(s) // isolated (tests)
	}
	sharedBreakersMu.Lock()
	defer sharedBreakersMu.Unlock()
	if cb, ok := sharedBreakers[s.Component]; ok {
		return cb
	}
	cb := newBreaker(s)
	sharedBreakers[s.Component] = cb
	return cb
}

func newBreaker(s ResilienceSettings) *gobreaker.CircuitBreaker {
	st := gobreaker.Settings{}
	st.Name = s.Component
	st.MaxRequests = s.CBHalfOpenMaxRequests
	st.Interval = s.CBInterval
	st.Timeout = s.CBOpenTimeout
	if s.CBTripAfterConsecutiveFailures > 0 {
		n := s.CBTripAfterConsecutiveFailures
		st.ReadyToTrip = func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= n
		}
	}
	comp := s.Component
	st.OnStateChange = func(_ string, from gobreaker.State, to gobreaker.State) {
		metrics.OutboundHTTPCircuitBreakerTransitionsTotal.WithLabelValues(comp, from.String(), to.String()).Inc()
		metrics.OutboundHTTPCircuitBreakerState.WithLabelValues(comp).Set(cbStateValue(to))
	}
	// Initial state — gobreaker starts closed; record so the gauge isn't blank
	// until the first failure. Runs once per component (registry), so later
	// client constructions can no longer reset an open breaker's gauge.
	metrics.OutboundHTTPCircuitBreakerState.WithLabelValues(comp).Set(cbStateValue(gobreaker.StateClosed))
	return gobreaker.NewCircuitBreaker(st)
}

func newCircuitBreakerRoundTripper(s ResilienceSettings, next http.RoundTripper) http.RoundTripper {
	return &circuitBreakerRoundTripper{
		cb:   sharedBreaker(s),
		next: next,
	}
}

// cbStateValue maps gobreaker states to numeric gauge values so dashboards can
// render "current state" without joining transition counters.
//
//	0 = closed, 1 = open, 2 = half-open
func cbStateValue(s gobreaker.State) float64 {
	switch s {
	case gobreaker.StateOpen:
		return 1
	case gobreaker.StateHalfOpen:
		return 2
	default:
		return 0
	}
}

// errUpstream5xx is an internal sentinel: the callback returns it alongside a
// real 5xx response so gobreaker records a failure, then RoundTrip unwraps it and
// hands the caller the real response. Without this the breaker only ever sees
// transport-level errors and never opens against an upstream that is hard-down
// but still answering 500/503.
var errUpstream5xx = errors.New("upstream returned 5xx (counted as circuit-breaker failure)")

func (c *circuitBreakerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.next == nil {
		c.next = http.DefaultTransport
	}
	v, err := c.cb.Execute(func() (interface{}, error) {
		resp, rerr := c.next.RoundTrip(req)
		if rerr != nil {
			return nil, rerr
		}
		if resp.StatusCode >= 500 {
			// Count as a failure but preserve the response in the returned value.
			return resp, errUpstream5xx
		}
		return resp, nil
	})
	if err != nil {
		if errors.Is(err, errUpstream5xx) {
			return v.(*http.Response), nil // deliver the real 5xx to the caller
		}
		return nil, err // transport error, or ErrOpenState/ErrTooManyRequests
	}
	return v.(*http.Response), nil
}
