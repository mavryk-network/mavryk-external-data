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

	// FXConversionDurationSeconds — wall time of one PriceConverter.Convert
	// call (cache hit or miss). Histogram per (source_token, target).
	FXConversionDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "fx_conversion_duration_seconds",
			Help:    "Wall time of one FX conversion (cache hit + miss combined).",
			Buckets: rateLimitBuckets,
		},
		[]string{"source_token", "target"},
	)

	// FXConversionsTotal — counter labeled by outcome:
	// `success`, `identity`, `no_rate`, `unsupported_target`, `unregistered_source`, `query_error`.
	FXConversionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fx_conversions_total",
			Help: "Outcome of FX conversions exposed at API edges.",
		},
		[]string{"source_token", "target", "result"},
	)

	// FXStaleResponsesTotal — counter of `?in=` responses that were served
	// with `fx.stale=true`. A growing rate means CoinGecko live-job is
	// behind or down — alert on `rate(...) > 1% of total`.
	FXStaleResponsesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fx_stale_responses_total",
			Help: "Conversions returned with stale FX (older than configured budget).",
		},
		[]string{"target"},
	)

	// ChartQueryDurationSeconds — wall time of one chart query (Series/OHLC
	// over CandleRepository). Per (kind=fa|rwa, interval).
	ChartQueryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "chart_query_duration_seconds",
			Help:    "Wall time of one chart query, per kind+interval.",
			Buckets: durationBuckets,
		},
		[]string{"kind", "interval"},
	)

	// ChartQueryRows — number of candles/points returned per chart query.
	// High values combined with cap-hits suggest a client over-pulling.
	ChartQueryRows = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "chart_query_rows",
			Help:    "Rows returned per chart query, per kind+interval.",
			Buckets: []float64{1, 10, 100, 500, 1000, 2500, 5000, 8000, 10000},
		},
		[]string{"kind", "interval"},
	)

	// ChartQueryCapHitsTotal — counter of requests rejected by the
	// per-interval window cap (see ADR-0015). reason=range_exceeded means
	// (to-from) > cap[interval]; reason=limit means ?limit > MaxLimit.
	// A growing rate is a UX signal — clients are asking for more than the
	// caps allow and need pagination.
	ChartQueryCapHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "chart_query_cap_hits_total",
			Help: "Chart requests rejected because they exceeded server-side caps.",
		},
		[]string{"kind", "interval", "reason"},
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
