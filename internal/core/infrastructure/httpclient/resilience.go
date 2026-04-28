package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/sony/gobreaker"

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

// WrapResilientTransport returns base wrapped with retry, then optionally a circuit breaker (outermost).
// Order: circuit breaker → retry → base.
func WrapResilientTransport(base http.RoundTripper, s ResilienceSettings) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if s.CircuitBreakerDisabled && s.RetryMaxAttempts == 1 {
		return base
	}
	s = s.Normalized()
	rt := http.RoundTripper(&retryTransport{next: base, s: s})
	if !s.CircuitBreakerDisabled {
		rt = newCircuitBreakerRoundTripper(s, rt)
	}
	return rt
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
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
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
			continue
		}
		if shouldRetryHTTPStatus(resp.StatusCode) {
			if attempt >= attempts {
				return resp, nil
			}
			_ = resp.Body.Close()
			if comp != "" {
				metrics.OutboundHTTPRetriesTotal.WithLabelValues(comp).Inc()
			}
			if werr := waitContext(req.Context(), bo.NextBackOff()); werr != nil {
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

func newCircuitBreakerRoundTripper(s ResilienceSettings, next http.RoundTripper) http.RoundTripper {
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
	// until the first failure.
	metrics.OutboundHTTPCircuitBreakerState.WithLabelValues(comp).Set(cbStateValue(gobreaker.StateClosed))
	return &circuitBreakerRoundTripper{
		cb:   gobreaker.NewCircuitBreaker(st),
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

func (c *circuitBreakerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if c.next == nil {
		c.next = http.DefaultTransport
	}
	v, err := c.cb.Execute(func() (interface{}, error) {
		return c.next.RoundTrip(req)
	})
	if err != nil {
		return nil, err
	}
	return v.(*http.Response), nil
}
