package httpclient

import (
	"net/http"
	"sync"
	"time"
)

// TransportSettings configures the pooled HTTP transport. Zero values are
// replaced with sensible defaults in Normalized().
type TransportSettings struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration
	TLSHandshakeTimeout time.Duration
}

// Normalized fills zero fields with defaults.
func (s TransportSettings) Normalized() TransportSettings {
	if s.MaxIdleConns <= 0 {
		s.MaxIdleConns = 100
	}
	if s.MaxIdleConnsPerHost <= 0 {
		s.MaxIdleConnsPerHost = 10
	}
	if s.MaxConnsPerHost <= 0 {
		s.MaxConnsPerHost = 100
	}
	if s.IdleConnTimeout <= 0 {
		s.IdleConnTimeout = 90 * time.Second
	}
	if s.TLSHandshakeTimeout <= 0 {
		s.TLSHandshakeTimeout = 10 * time.Second
	}
	return s
}

// NewPooledTransport creates an HTTP transport with the given pool settings.
// Empty/zero settings produce defaults; see TransportSettings.Normalized().
func NewPooledTransport(s TransportSettings) *http.Transport {
	s = s.Normalized()
	return &http.Transport{
		// Honor HTTP(S)_PROXY / NO_PROXY like http.DefaultTransport does. Custom
		// transports don't set this implicitly, so in proxy-only egress clusters
		// every outbound call would otherwise fail with opaque dial timeouts.
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        s.MaxIdleConns,
		MaxIdleConnsPerHost: s.MaxIdleConnsPerHost,
		MaxConnsPerHost:     s.MaxConnsPerHost,
		IdleConnTimeout:     s.IdleConnTimeout,
		TLSHandshakeTimeout: s.TLSHandshakeTimeout,
		DisableCompression:  false,
	}
}

var (
	sharedTransportOnce sync.Once
	sharedTransport     *http.Transport
)

// ConfigureSharedTransport sets up the process-wide pooled transport. Safe to
// call once at startup; subsequent calls are ignored. Tests that need a fresh
// transport should construct their own via NewPooledTransport.
func ConfigureSharedTransport(s TransportSettings) {
	sharedTransportOnce.Do(func() {
		sharedTransport = NewPooledTransport(s)
	})
}

// SharedTransport returns the application-wide shared HTTP transport, lazily
// initializing with defaults when ConfigureSharedTransport was never called.
func SharedTransport() *http.Transport {
	if sharedTransport == nil {
		sharedTransportOnce.Do(func() {
			sharedTransport = NewPooledTransport(TransportSettings{})
		})
	}
	return sharedTransport
}
