package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sony/gobreaker"
	"golang.org/x/time/rate"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mkResp(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}
}

// TestCircuitBreakerTripsOn5xx pins the fix: an upstream that keeps answering 500
// (hard-down but responsive) must open the breaker. Previously only transport
// errors counted, so the breaker never engaged during such an outage.
func TestCircuitBreakerTripsOn5xx(t *testing.T) {
	ResetSharedBreakers()
	var calls int
	base := rtFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return mkResp(500), nil
	})
	s := ResilienceSettings{
		Component:                      "test-cb-5xx",
		RetryMaxAttempts:               1,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	}
	rt := WrapCircuitBreaker(WrapResilientTransport(base, s), s)
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)

	for i := 0; i < 2; i++ {
		r, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("call %d: unexpected err %v", i, err)
		}
		if r.StatusCode != 500 {
			t.Fatalf("call %d: want 500, got %d", i, r.StatusCode)
		}
	}
	if _, err := rt.RoundTrip(req); !errors.Is(err, gobreaker.ErrOpenState) {
		t.Fatalf("3rd call: want ErrOpenState, got %v", err)
	}
	if calls != 2 {
		t.Errorf("base hit %d times, want 2 (3rd short-circuited by open breaker)", calls)
	}
}

// TestCircuitBreakerIgnores4xx: client errors must not open the breaker.
func TestCircuitBreakerIgnores4xx(t *testing.T) {
	ResetSharedBreakers()
	base := rtFunc(func(_ *http.Request) (*http.Response, error) { return mkResp(400), nil })
	s := ResilienceSettings{
		Component:                      "test-cb-4xx",
		RetryMaxAttempts:               1,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	}
	rt := WrapCircuitBreaker(WrapResilientTransport(base, s), s)
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	for i := 0; i < 5; i++ {
		r, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("400 must not open breaker (call %d err %v)", i, err)
		}
		if r.StatusCode != 400 {
			t.Fatalf("want 400, got %d", r.StatusCode)
		}
	}
}

// TestRateLimiterWaitDoesNotTripBreaker pins the layering rule in
// WrapCircuitBreaker: the limiter must sit OUTSIDE the breaker so that
// self-inflicted throttling failures — a Wait that fast-fails because the
// caller's deadline is shorter than the queue delay, with the upstream never
// contacted — cannot be mistaken for upstream failures. With the breaker
// outermost, gobreaker counted each of these as a failure and tripped against
// a perfectly healthy upstream, fast-failing every request for the component.
func TestRateLimiterWaitDoesNotTripBreaker(t *testing.T) {
	ResetSharedLimiters()
	ResetSharedBreakers()
	var upstreamCalls int
	base := rtFunc(func(_ *http.Request) (*http.Response, error) {
		upstreamCalls++
		return mkResp(200), nil
	})
	s := ResilienceSettings{
		Component:                      "test-cb-limiter",
		RetryMaxAttempts:               1,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	}
	// Burst 1 at 0.01 RPS: the first request passes, every later one must wait
	// ~100s for a token.
	rl := RateLimitSettings{Component: "test-cb-limiter", RPS: 0.01, Burst: 1}
	rt := WrapRateLimited(WrapCircuitBreaker(WrapResilientTransport(base, s), s), rl)

	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("first request should consume the burst token: %v", err)
	}

	// Exhaust well past the trip threshold with deadline-bound requests the
	// limiter cannot satisfy.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := rt.RoundTrip(req.WithContext(ctx))
		cancel()
		if err == nil {
			t.Fatalf("throttled request %d unexpectedly succeeded", i)
		}
		if errors.Is(err, gobreaker.ErrOpenState) {
			t.Fatalf("throttled request %d saw an OPEN breaker: limiter waits are being counted as upstream failures", i)
		}
	}

	// The upstream was never contacted for those, and the breaker must still be
	// closed: a request that does get a token has to reach the upstream.
	if upstreamCalls != 1 {
		t.Fatalf("upstream calls = %d, want 1 (throttled requests never reach it)", upstreamCalls)
	}
	limiter := lookupSharedLimiter("test-cb-limiter")
	if limiter == nil {
		t.Fatal("shared limiter not registered")
	}
	limiter.SetLimit(rate.Inf)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("breaker must still be closed after throttling, got %v", err)
	}
	if upstreamCalls != 2 {
		t.Errorf("upstream calls = %d, want 2", upstreamCalls)
	}
}

// TestRetrySideLimiterWaitDoesNotTripBreaker covers the path the layering rule
// alone cannot protect: retryTransport runs INSIDE the breaker and charges the
// shared limiter per retry, so a retry it cannot stage must surface the real
// upstream outcome. A pure 429 storm (never a failure by the breaker's own
// 4xx rule) previously turned into a throttling error inside cb.Execute and
// opened the breaker with no upstream failure at all.
func TestRetrySideLimiterWaitDoesNotTripBreaker(t *testing.T) {
	ResetSharedLimiters()
	ResetSharedBreakers()
	const comp = "test-cb-retry-limiter"
	base := rtFunc(func(_ *http.Request) (*http.Response, error) { return mkResp(429), nil })
	s := ResilienceSettings{
		Component:                      comp,
		RetryMaxAttempts:               3,
		RetryInitialWait:               time.Millisecond,
		RetryMaxWait:                   time.Millisecond,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	}
	// Register the component's shared limiter (the one retryTransport consults)
	// and drain its single token, so every retry inside the breaker fails to
	// acquire one. No outer WrapRateLimited here: this test isolates the
	// retry-side path, which the layering rule alone cannot protect.
	limiter := sharedLimiter(comp, 0.01, 1)
	limiter.Allow()
	rt := WrapCircuitBreaker(WrapResilientTransport(base, s), s)

	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		resp, err := rt.RoundTrip(req.WithContext(ctx))
		cancel()
		if errors.Is(err, gobreaker.ErrOpenState) {
			t.Fatalf("request %d: breaker opened on a pure 429 storm — retry-side limiter waits are reaching it", i)
		}
		if err != nil {
			t.Fatalf("request %d: want the real upstream 429, got error %v", i, err)
		}
		if resp.StatusCode != 429 {
			t.Fatalf("request %d: status = %d, want the real upstream 429", i, resp.StatusCode)
		}
	}
}

func TestRetryableErr(t *testing.T) {
	// Permanent/deterministic failures must NOT be retried.
	if retryableErr(&tls.CertificateVerificationError{}) {
		t.Error("TLS cert verification error must be non-retryable")
	}
	if retryableErr(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Error("DNS NXDOMAIN must be non-retryable")
	}
	schemeErr := &url.Error{Op: "Get", URL: "ftp://x", Err: fmt.Errorf("unsupported protocol scheme %q", "ftp")}
	if retryableErr(schemeErr) {
		t.Error("unsupported protocol scheme must be non-retryable")
	}
	// Transient failures should still retry.
	if !retryableErr(&net.DNSError{Err: "server misbehaving", IsTemporary: true}) {
		t.Error("temporary DNS error should be retryable")
	}
	if !retryableErr(fmt.Errorf("connection reset by peer")) {
		t.Error("generic transport error should be retryable")
	}
}

func TestRetryAfter(t *testing.T) {
	mk := func(v string) *http.Response {
		h := make(http.Header)
		if v != "" {
			h.Set("Retry-After", v)
		}
		return &http.Response{Header: h}
	}
	if got := retryAfter(mk("2")); got != 2*time.Second {
		t.Errorf("delta-seconds: got %v want 2s", got)
	}
	if got := retryAfter(mk("0")); got != 0 {
		t.Errorf("zero: got %v want 0", got)
	}
	if got := retryAfter(mk("")); got != 0 {
		t.Errorf("absent: got %v want 0", got)
	}
	if got := retryAfter(mk("garbage")); got != 0 {
		t.Errorf("garbage: got %v want 0", got)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfter(mk(future)); got <= 0 || got > 31*time.Second {
		t.Errorf("http-date future: got %v want ~30s", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := retryAfter(mk(past)); got != 0 {
		t.Errorf("http-date past: got %v want 0", got)
	}
}
