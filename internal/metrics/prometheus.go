// Package metrics registers Prometheus collectors for the service.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	durationBuckets  = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	jobBuckets       = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
	rateLimitBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
)

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

	// OutboundHTTPCircuitBreakerState is the current CB state per component.
	// 0 = closed, 1 = open, 2 = half-open. Updated by the CB layer on transitions.
	OutboundHTTPCircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "outbound_http_circuit_breaker_state",
			Help: "Current circuit breaker state (0=closed, 1=open, 2=half-open).",
		},
		[]string{"component"},
	)

	OutboundHTTPRateLimitWaitSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "outbound_http_rate_limit_wait_seconds",
			Help:    "Time spent waiting for an outbound HTTP rate-limit token.",
			Buckets: rateLimitBuckets,
		},
		[]string{"component"},
	)

	// JobTickDurationSeconds — time taken for one tick of a background job.
	JobTickDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "job_tick_duration_seconds",
			Help:    "Time spent in one tick of a background job.",
			Buckets: jobBuckets,
		},
		[]string{"job", "source", "entity"},
	)

	// JobErrorsTotal — counter of tick-level errors per (job, source, reason).
	JobErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_errors_total",
			Help: "Errors per background-job tick by reason.",
		},
		[]string{"job", "source", "entity", "reason"},
	)

	// JobRowsAffectedTotal — total rows written by a job (Save() return).
	JobRowsAffectedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_rows_affected_total",
			Help: "Cumulative rows affected by a background job's Save calls.",
		},
		[]string{"job", "source", "entity"},
	)

	// BackfillOldestTsSeconds — Unix timestamp of the current oldest_ts cursor.
	BackfillOldestTsSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "backfill_oldest_ts_seconds",
			Help: "Current oldest_ts cursor (Unix seconds) for a backfill entity. Updated after each successful step.",
		},
		[]string{"source", "entity"},
	)

	// BackfillAutoDisabledTotal — counter of times an entity has been auto-disabled.
	BackfillAutoDisabledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "backfill_auto_disabled_total",
			Help: "Backfill auto-disable events by entity.",
		},
		[]string{"source", "entity", "reason"},
	)

	// DBOpenConnections / DBInUseConnections — sql.DB pool stats. Exported via a
	// background goroutine (or pull collector) that reads db.Stats() periodically.
	DBOpenConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "db_pool_open_connections",
			Help: "Current open connections to the database pool.",
		},
	)
	DBInUseConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "db_pool_in_use_connections",
			Help: "Connections currently in use.",
		},
	)
	DBIdleConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "db_pool_idle_connections",
			Help: "Idle connections.",
		},
	)
	DBWaitDurationSeconds = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "db_pool_wait_duration_seconds",
			Help: "Cumulative seconds blocked waiting for a free connection.",
		},
	)
)

// StatusClass returns 2xx, 4xx, or 5xx for HTTPResponsesTotal labelling.
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
