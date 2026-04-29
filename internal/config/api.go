package config

import (
	"time"

	"quotes/internal/core/infrastructure/httpclient"
)

// APIConfig holds shared outbound HTTP settings (timeout, retry, circuit breaker,
// transport pooling) applied to every third-party client. Per-service rate limits
// live in the service configs (see CoinGeckoConfig.RateLimit, EquiteezConfig.RateLimit).
type APIConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`

	// Transport pooling. 0 keeps the in-code defaults
	// (max_idle_conns=100, max_idle_conns_per_host=10, max_conns_per_host=100,
	// idle_conn_timeout=90s, tls_handshake_timeout=10s).
	TransportMaxIdleConns        int          `yaml:"transport_max_idle_conns"`
	TransportMaxIdleConnsPerHost int          `yaml:"transport_max_idle_conns_per_host"`
	TransportMaxConnsPerHost     int          `yaml:"transport_max_conns_per_host"`
	TransportIdleConnTimeout     DurationYAML `yaml:"transport_idle_conn_timeout"`
	TransportTLSHandshakeTimeout DurationYAML `yaml:"transport_tls_handshake_timeout"`

	// Max bytes accepted from any single outbound HTTP response (post-decompress).
	// 0 disables the cap. Recommended production: 16 << 20 (16 MiB).
	OutboundMaxResponseBytes int64 `yaml:"outbound_max_response_bytes"`

	OutboundHTTPRetryMaxAttempts int `yaml:"outbound_http_retry_max_attempts"`
	OutboundHTTPRetryInitialMS   int `yaml:"outbound_http_retry_initial_ms"`
	OutboundHTTPRetryMaxMS       int `yaml:"outbound_http_retry_max_ms"`

	OutboundHTTPCircuitBreakerDisabled            bool   `yaml:"outbound_http_circuit_breaker_disabled"`
	OutboundHTTPCircuitBreakerIntervalSeconds     int    `yaml:"outbound_http_circuit_breaker_interval_seconds"`
	OutboundHTTPCircuitBreakerOpenSeconds         int    `yaml:"outbound_http_circuit_breaker_open_seconds"`
	OutboundHTTPCircuitBreakerHalfOpenMaxRequests uint32 `yaml:"outbound_http_circuit_breaker_half_open_max_requests"`
	OutboundHTTPCircuitBreakerTripAfterFailures   uint32 `yaml:"outbound_http_circuit_breaker_trip_after_failures"`
}

// OutboundResilience builds retry + circuit-breaker settings tagged with the
// given component name (used for metrics labels and the CB registry key).
func (c *APIConfig) OutboundResilience(component string) httpclient.ResilienceSettings {
	if c == nil {
		return httpclient.ResilienceSettings{Component: component}.Normalized()
	}
	s := httpclient.ResilienceSettings{
		Component:                      component,
		RetryMaxAttempts:               c.OutboundHTTPRetryMaxAttempts,
		CircuitBreakerDisabled:         c.OutboundHTTPCircuitBreakerDisabled,
		CBHalfOpenMaxRequests:          c.OutboundHTTPCircuitBreakerHalfOpenMaxRequests,
		CBTripAfterConsecutiveFailures: c.OutboundHTTPCircuitBreakerTripAfterFailures,
	}
	if c.OutboundHTTPRetryInitialMS > 0 {
		s.RetryInitialWait = time.Duration(c.OutboundHTTPRetryInitialMS) * time.Millisecond
	}
	if c.OutboundHTTPRetryMaxMS > 0 {
		s.RetryMaxWait = time.Duration(c.OutboundHTTPRetryMaxMS) * time.Millisecond
	}
	if c.OutboundHTTPCircuitBreakerIntervalSeconds > 0 {
		s.CBInterval = time.Duration(c.OutboundHTTPCircuitBreakerIntervalSeconds) * time.Second
	}
	if c.OutboundHTTPCircuitBreakerOpenSeconds > 0 {
		s.CBOpenTimeout = time.Duration(c.OutboundHTTPCircuitBreakerOpenSeconds) * time.Second
	}
	return s.Normalized()
}
