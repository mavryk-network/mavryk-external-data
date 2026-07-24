package httpclient

import (
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
	var calls int
	base := rtFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return mkResp(500), nil
	})
	rt := WrapResilientTransport(base, ResilienceSettings{
		Component:                      "test-cb-5xx",
		RetryMaxAttempts:               1,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	})
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
	base := rtFunc(func(_ *http.Request) (*http.Response, error) { return mkResp(400), nil })
	rt := WrapResilientTransport(base, ResilienceSettings{
		Component:                      "test-cb-4xx",
		RetryMaxAttempts:               1,
		CBTripAfterConsecutiveFailures: 2,
		CBInterval:                     time.Minute,
		CBOpenTimeout:                  time.Minute,
	})
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
