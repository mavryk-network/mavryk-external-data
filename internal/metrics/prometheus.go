// Package metrics registers Prometheus collectors for the quotes service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

var (
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by method and route template.",
		},
		[]string{"method", "path"},
	)

	HTTPRequestDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: durationBuckets,
		},
		[]string{"method", "path"},
	)

	HTTPResponsesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_responses_total",
			Help: "HTTP responses by method, route template, and status class.",
		},
		[]string{"method", "path", "status_class"},
	)

	OutboundHTTPRetriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbound_http_retries_total",
			Help: "Extra outbound HTTP attempts after the first try (retry layer).",
		},
		[]string{"component"},
	)

	OutboundHTTPCircuitBreakerTransitionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbound_http_circuit_breaker_transitions_total",
			Help: "Circuit breaker state transitions for outbound HTTP clients.",
		},
		[]string{"component", "from_state", "to_state"},
	)

	OutboundHTTPRateLimitWaitSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "outbound_http_rate_limit_wait_seconds",
			Help:    "Time spent waiting for an outbound HTTP rate-limit token.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		},
		[]string{"component"},
	)
)

// StatusClass returns 2xx, 4xx, or 5xx for HTTPResponsesTotal.
func StatusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	default:
		return "2xx"
	}
}
