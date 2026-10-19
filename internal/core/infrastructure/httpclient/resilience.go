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

// WrapResilientTransport wraps base with retry → counter → base. The counter
// sits inside retry so every attempt increments outbound_http_requests_total.
// The circuit breaker is applied separately (see WrapCircuitBreaker).
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

// WrapCircuitBreaker wraps rt with the component's shared breaker (no-op when
// disabled). Must be applied UNDER the rate limiter: gobreaker counts every
// non-nil error as an upstream failure, so a limiter Wait that fast-fails
// would trip the breaker on purely self-inflicted throttling. The cost is that
// an open breaker still spends a limiter token before fast-failing.
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
			// Failing to stage the retry is OUR constraint, not the upstream's:
			// surface the upstream error so the breaker judges only upstream.
			if werr := waitContext(req.Context(), bo.NextBackOff()); werr != nil {
				return nil, err
			}
			if werr := limiterWait(req.Context(), lim, comp); werr != nil {
				return nil, err
			}
			if comp != "" {
				metrics.OutboundHTTPRetriesTotal.WithLabelValues(comp).Inc()
			}
			continue
		}
		if shouldRetryHTTPStatus(resp.StatusCode) {
			if attempt >= attempts {
				return resp, nil
			}
			// Retry-After (delta-seconds or HTTP-date) wins over backoff.
			wait := bo.NextBackOff()
			if ra := retryAfter(resp); ra > 0 {
				wait = ra
			}
			// Buffer and close BEFORE waiting: an unclosed body pins its pooled
			// connection for the whole (minutes-long) Retry-After. The copy
			// keeps the response returnable if the retry can't be staged.
			bufferErrorBody(resp)
			if werr := waitContext(req.Context(), wait); werr != nil {
				return resp, nil
			}
			if werr := limiterWait(req.Context(), lim, comp); werr != nil {
				return resp, nil
			}
			if comp != "" {
				metrics.OutboundHTTPRetriesTotal.WithLabelValues(comp).Inc()
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

// sharedBreakers holds one breaker per component (mirroring sharedLimiters):
// per-instance breakers would each need their own failure streak to trip, and
// a client rebuilt per tick could never trip at all. First construction wins.
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

// ResetSharedBreakers clears the registry so breakers start closed again.
// Test helper (mirrors ResetSharedLimiters); production never calls it.
func ResetSharedBreakers() {
	sharedBreakersMu.Lock()
	defer sharedBreakersMu.Unlock()
	for k := range sharedBreakers {
		delete(sharedBreakers, k)
	}
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
	// Seed the gauge so it isn't blank until the first failure.
	metrics.OutboundHTTPCircuitBreakerState.WithLabelValues(comp).Set(cbStateValue(gobreaker.StateClosed))
	return gobreaker.NewCircuitBreaker(st)
}

func newCircuitBreakerRoundTripper(s ResilienceSettings, next http.RoundTripper) http.RoundTripper {
	return &circuitBreakerRoundTripper{
		cb:   sharedBreaker(s),
		next: next,
	}
}

// cbStateValue maps breaker states to gauge values: 0 closed, 1 open, 2 half-open.
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

const maxBufferedErrorBody = 32 << 10

// bufferErrorBody swaps resp.Body for an in-memory copy and closes the
// original, releasing its pooled connection across the retry wait.
func bufferErrorBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, maxBufferedErrorBody))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(buf))
	// A truncated body under the original ContentLength mis-frames Response.Write.
	resp.ContentLength = int64(len(buf))
}

// errUpstream5xx makes gobreaker count a 5xx as a failure; RoundTrip unwraps it
// and returns the real response. Without it the breaker never opens against an
// upstream that is hard-down but still answering 500/503.
var errUpstream5xx = errors.New("upstream returned 5xx (counted as circuit-breaker failure)")

func (c *circuitBreakerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.next == nil {
		c.next = http.DefaultTransport
	}
	// An already-cancelled caller (job shutdown, expired tick budget) says
	// nothing about upstream health. Fail before Execute so gobreaker — which
	// has no "neutral" outcome — does not record it as a failure and inch the
	// shared breaker toward tripping during an ordinary drain.
	if err := req.Context().Err(); err != nil {
		return nil, err
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
