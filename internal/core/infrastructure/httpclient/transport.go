package httpclient

import (
	"net/http"
	"time"
)

// NewPooledTransport creates an HTTP transport with optimized connection pooling.
func NewPooledTransport() *http.Transport {
	return &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  false,
	}
}

var sharedTransport = NewPooledTransport()

// SharedTransport returns the application-wide shared HTTP transport.
func SharedTransport() *http.Transport {
	return sharedTransport
}
