package config

import (
	"time"

	"quotes/internal/core/infrastructure/httpclient"
)

// APIConfig holds outbound HTTP settings for CoinGecko (timeout, rate limit, retry, circuit breaker).
type APIConfig struct {
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// RateLimitRPS is a proactive token-bucket limit (req/s) for the CoinGecko HTTP client. 0 disables.
	RateLimitRPS   int `yaml:"rate_limit_rps"`
	RateLimitBurst int `yaml:"rate_limit_burst"` // 0 = derive from RPS (2× rounded)

	OutboundHTTPRetryMaxAttempts int `yaml:"outbound_http_retry_max_attempts"`
	OutboundHTTPRetryInitialMS   int `yaml:"outbound_http_retry_initial_ms"`
	OutboundHTTPRetryMaxMS       int `yaml:"outbound_http_retry_max_ms"`

	OutboundHTTPCircuitBreakerDisabled            bool   `yaml:"outbound_http_circuit_breaker_disabled"`
	OutboundHTTPCircuitBreakerIntervalSeconds     int    `yaml:"outbound_http_circuit_breaker_interval_seconds"`
	OutboundHTTPCircuitBreakerOpenSeconds         int    `yaml:"outbound_http_circuit_breaker_open_seconds"`
	OutboundHTTPCircuitBreakerHalfOpenMaxRequests uint32 `yaml:"outbound_http_circuit_breaker_half_open_max_requests"`
	OutboundHTTPCircuitBreakerTripAfterFailures   uint32 `yaml:"outbound_http_circuit_breaker_trip_after_failures"`
}

const coingeckoComponent = "coingecko"

// CoinGeckoRateLimit builds token-bucket settings for the CoinGecko client from api.* fields.
func (c *APIConfig) CoinGeckoRateLimit() httpclient.RateLimitSettings {
	if c == nil || c.RateLimitRPS <= 0 {
		return httpclient.RateLimitSettings{Component: coingeckoComponent}
	}
	rps := float64(c.RateLimitRPS)
	burst := c.RateLimitBurst
	if burst <= 0 {
		burst = int(rps*2 + 0.5)
		if burst < 1 {
			burst = 1
		}
	}
	return httpclient.RateLimitSettings{
		Component: coingeckoComponent,
		RPS:       rps,
		Burst:     burst,
	}
}

// CoinGeckoOutboundResilience builds retry + circuit breaker settings for CoinGecko.
func (c *APIConfig) CoinGeckoOutboundResilience() httpclient.ResilienceSettings {
	if c == nil {
		return httpclient.ResilienceSettings{Component: coingeckoComponent}.Normalized()
	}
	s := httpclient.ResilienceSettings{
		Component:                      coingeckoComponent,
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
