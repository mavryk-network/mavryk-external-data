package httpclient

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

func bodyTransport(n int) rtFunc {
	return rtFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), n))),
			Header:     make(http.Header),
		}, nil
	})
}

func TestMaxBytesReader_ExactSizeAllowed(t *testing.T) {
	rt := MaxBytesReader(bodyTransport(100), 100)
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("a body of exactly maxBytes must succeed, got %v", err)
	}
	if len(b) != 100 {
		t.Fatalf("read %d bytes, want 100", len(b))
	}
}

func TestMaxBytesReader_OverSizeRejected(t *testing.T) {
	rt := MaxBytesReader(bodyTransport(101), 100)
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge for maxBytes+1, got %v", err)
	}
}
