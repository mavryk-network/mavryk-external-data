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

	// One increment per network attempt, so retries count separately:
	// `total - outbound_http_retries_total` ≈ logical requests from callers.
	// outcome ∈ {2xx, 4xx, 5xx, error}; `error` = no HTTP status received.
	OutboundHTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbound_http_requests_total",
			Help: "Total outbound HTTP attempts (post-retry layer), labelled by component and outcome class.",
		},
		[]string{"component", "outcome"},
	)

	OutboundHTTPCircuitBreakerTransitionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "outbound_http_circuit_breaker_transitions_total",
			Help: "Circuit breaker state transitions for outbound HTTP clients.",
		},
		[]string{"component", "from_state", "to_state"},
	)

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

	JobTickDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "job_tick_duration_seconds",
			Help:    "Time spent in one tick of a background job.",
			Buckets: jobBuckets,
		},
		[]string{"job", "source", "entity"},
	)

	JobErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_errors_total",
			Help: "Errors per background-job tick by reason.",
		},
		[]string{"job", "source", "entity", "reason"},
	)

	// Values dropped instead of failing the batch: the upstream sent something
	// unstorable (non-finite, or beyond numeric(38,18)). Routine skips such as a
	// zero bid are NOT counted, so alert on any increase.
	IngestRowsDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ingest_rows_dropped_total",
			Help: "Upstream values discarded during row mapping, by source, entity and reason.",
		},
		[]string{"source", "entity", "reason"},
	)

	// Advances only on a tick that reported no error and did not panic, so a
	// gauge that stops moving means stalled OR failing; alert on now() - gauge >
	// a few intervals. Seeded at job start. An empty-but-200 upstream still
	// counts as success here — pair with job_rows_affected_total.
	JobLastSuccessTimestamp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "job_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last background-job tick that completed its work without error.",
		},
		[]string{"job"},
	)

	// Non-zero means a tick panicked but runTickerLoop survived; alert on any
	// increase.
	JobTickPanicsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_tick_panics_total",
			Help: "Panics recovered within a background-job tick (loop kept running).",
		},
		[]string{"job"},
	)

	JobRowsAffectedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_rows_affected_total",
			Help: "Cumulative rows affected by a background job's Save calls.",
		},
		[]string{"job", "source", "entity"},
	)

	BackfillOldestTsSeconds = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "backfill_oldest_ts_seconds",
			Help: "Current oldest_ts cursor (Unix seconds) for a backfill entity. Updated after each successful step.",
		},
		[]string{"source", "entity"},
	)

	// Counts entities crossing BackfillMaxErrors. The "disabled" name is kept for
	// dashboard compatibility; crossing only parks the entity for a cooldown.
	BackfillAutoDisabledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "backfill_auto_disabled_total",
			Help: "Backfill error-threshold events by entity (entity parked for a cooldown, not permanently disabled).",
		},
		[]string{"source", "entity", "reason"},
	)

	// sql.DB pool stats, refreshed from db.Stats() by a background goroutine.
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

	FXConversionDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "fx_conversion_duration_seconds",
			Help:    "Wall time of one FX conversion (cache hit + miss combined).",
			Buckets: rateLimitBuckets,
		},
		[]string{"source_token", "target"},
	)

	// result ∈ {success, identity, no_rate, unsupported_target,
	// unregistered_source, query_error}.
	FXConversionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fx_conversions_total",
			Help: "Outcome of FX conversions exposed at API edges.",
		},
		[]string{"source_token", "target", "result"},
	)

	// A growing rate means the CoinGecko live job is behind or down; alert above
	// ~1% of total conversions.
	FXStaleResponsesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fx_stale_responses_total",
			Help: "Conversions returned with stale FX (older than configured budget).",
		},
		[]string{"target"},
	)

	// kind ∈ {fa, rwa}.
	ChartQueryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "chart_query_duration_seconds",
			Help:    "Wall time of one chart query, per kind+interval.",
			Buckets: durationBuckets,
		},
		[]string{"kind", "interval"},
	)

	// High values together with cap-hits suggest a client over-pulling.
	ChartQueryRows = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "chart_query_rows",
			Help:    "Rows returned per chart query, per kind+interval.",
			Buckets: []float64{1, 10, 100, 500, 1000, 2500, 5000, 8000, 10000},
		},
		[]string{"kind", "interval"},
	)

	// Per-interval window caps, see ADR-0015. reason=range_exceeded: (to-from) >
	// cap[interval]; reason=limit: ?limit > MaxLimit.
	ChartQueryCapHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "chart_query_cap_hits_total",
			Help: "Chart requests rejected because they exceeded server-side caps.",
		},
		[]string{"kind", "interval", "reason"},
	)

	// periods_count is a closed enum (1..4, bounded by the Period whitelist), so
	// cardinality stays predictable.
	ChangeQueryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "change_query_duration_seconds",
			Help:    "Wall time of one price-change query, per kind+periods_count.",
			Buckets: durationBuckets,
		},
		[]string{"kind", "periods_count"},
	)

	// result ∈ {hit, miss, singleflight_collapsed}, the last meaning the request
	// joined an in-flight repo call for the same key.
	ChangeQueryCacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "change_query_cache_hits_total",
			Help: "Cache slot outcomes per /change request, for hit-ratio dashboards.",
		},
		[]string{"kind", "result"},
	)

	// code ∈ {invalid_period, invalid_currency, invalid_argument, not_found,
	// repo_error, internal}. User input is never echoed, so labels stay bounded.
	ChangeQueryErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "change_query_errors_total",
			Help: "Failed /change requests by closed-enum reason.",
		},
		[]string{"kind", "code"},
	)

	// Set after each successful tick. A sharp fall means CoinGecko stopped
	// reporting pairs (delisting, paused market); alert on >~30% in 15min.
	TickersActiveCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tickers_active_count",
			Help: "Distinct (exchange, target_symbol) pairs in the last CoinGecko tickers tick, per token.",
		},
		[]string{"source", "token"},
	)

	// Coarser than TickersActiveCount: catches an exchange dropping the token
	// even when it listed a single pair.
	TickersExchangesCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tickers_exchanges_count",
			Help: "Distinct exchanges reporting the token in the last CoinGecko tickers tick.",
		},
		[]string{"source", "token"},
	)

	// endpoint ∈ {latest, distribution}; result ∈ {hit, miss}. Alert when the
	// per-endpoint hit ratio drops below ~0.5 (TTL too short, cache too small).
	TickersCacheRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "tickers_cache_requests_total",
			Help: "Outcome of tickers in-process cache lookups, per endpoint.",
		},
		[]string{"endpoint", "result"},
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

// OutboundOutcome maps a RoundTrip result to the OutboundHTTPRequestsTotal
// label; a non-nil err wins and status is ignored.
func OutboundOutcome(status int, err error) string {
	if err != nil {
		return "error"
	}
	return StatusClass(status)
}
