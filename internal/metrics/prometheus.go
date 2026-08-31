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

	// OutboundHTTPRequestsTotal counts every RoundTrip that actually hit the
	// network — one increment per attempt, so retries are counted separately.
	// `total - outbound_http_retries_total` ≈ logical requests issued by callers.
	// outcome ∈ {2xx, 4xx, 5xx, error}; `error` covers network/transport errors
	// where no HTTP status was received.
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

	// IngestRowsDroppedTotal — individual upstream values discarded during row
	// mapping instead of failing the whole batch. Non-zero means the upstream
	// sent something unstorable (a non-finite numeric, or a magnitude
	// numeric(38,18) cannot hold). Routine skips — a zero bid on a thin
	// orderbook — are NOT counted here, so alert on any increase.
	IngestRowsDroppedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ingest_rows_dropped_total",
			Help: "Upstream values discarded during row mapping, by source, entity and reason.",
		},
		[]string{"source", "entity", "reason"},
	)

	// JobLastSuccessTimestamp — unix time of the last tick that actually did its
	// work: the tick reported no error and did not panic. A tick that logged a
	// failed fetch or save, or in which every entity failed, leaves the gauge
	// where it was, so a job whose gauge stops advancing is stalled OR failing
	// (blocked query, dead upstream) even while the process looks healthy;
	// alert on now() - job_last_success_timestamp_seconds > a few intervals.
	// Seeded at job start so a job that never succeeds still exports a series.
	//
	// It answers "is the job erroring", not "is data arriving": an upstream that
	// answers 200 with an empty payload is a successful tick here. Pair it with
	// job_rows_affected_total to catch that.
	JobLastSuccessTimestamp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "job_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last background-job tick that completed its work without error.",
		},
		[]string{"job"},
	)

	// JobTickPanicsTotal — counter of panics recovered inside a single job tick.
	// A non-zero value means a tick paniced but the loop survived (see
	// runTickerLoop); alert on any increase.
	JobTickPanicsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "job_tick_panics_total",
			Help: "Panics recovered within a background-job tick (loop kept running).",
		},
		[]string{"job"},
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

	// BackfillAutoDisabledTotal — counter of times an entity crossed
	// BackfillMaxErrors. The name is kept for dashboard/alert compatibility;
	// crossing the threshold now parks the entity for backfillErrorCooldown
	// rather than disabling it permanently.
	BackfillAutoDisabledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "backfill_auto_disabled_total",
			Help: "Backfill error-threshold events by entity (entity parked for a cooldown, not permanently disabled).",
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

	// ChangeQueryDurationSeconds — wall time of one /change query end-to-end
	// (cache lookups + optional repo round-trip + compose). Per (kind=fa|rwa,
	// periods_count=1..4). periods_count is a closed enum (bounded by the
	// Period whitelist), keeps cardinality predictable.
	ChangeQueryDurationSeconds = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "change_query_duration_seconds",
			Help:    "Wall time of one price-change query, per kind+periods_count.",
			Buckets: durationBuckets,
		},
		[]string{"kind", "periods_count"},
	)

	// ChangeQueryCacheHitsTotal — outcome of every cache slot consulted
	// during a /change request. result is a closed enum:
	//   `hit`                     — entry was present and unexpired
	//   `miss`                    — entry was absent or expired
	//   `singleflight_collapsed`  — the request joined an in-flight
	//                               repo call for the same key (stampede
	//                               protection observed it).
	ChangeQueryCacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "change_query_cache_hits_total",
			Help: "Cache slot outcomes per /change request, for hit-ratio dashboards.",
		},
		[]string{"kind", "result"},
	)

	// ChangeQueryErrorsTotal — counter of failed /change requests, by
	// closed-enum classification. code is one of:
	//   `invalid_period`, `invalid_currency`, `invalid_argument`,
	//   `not_found`, `repo_error`, `internal`.
	// User input is never echoed — labels stay bounded.
	ChangeQueryErrorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "change_query_errors_total",
			Help: "Failed /change requests by closed-enum reason.",
		},
		[]string{"kind", "code"},
	)

	// TickersActiveCount — gauge of distinct (exchange, target_symbol) pairs
	// observed in the latest CoinGecko tickers tick, per token. Set after each
	// successful job tick. A drop from 50 → 30 between ticks means CoinGecko
	// stopped reporting ~20 pairs (exchange delisted, market paused) — alert
	// when this gauge falls more than ~30% in 15min.
	TickersActiveCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tickers_active_count",
			Help: "Distinct (exchange, target_symbol) pairs in the last CoinGecko tickers tick, per token.",
		},
		[]string{"source", "token"},
	)

	// TickersExchangesCount — gauge of distinct exchanges observed in the
	// latest tick, per token. Useful as a coarser signal than
	// TickersActiveCount (catches "Binance delisted MVRK" even if Binance
	// previously had only one pair).
	TickersExchangesCount = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "tickers_exchanges_count",
			Help: "Distinct exchanges reporting the token in the last CoinGecko tickers tick.",
		},
		[]string{"source", "token"},
	)

	// TickersCacheRequestsTotal — counter of cache outcomes for the in-process
	// snapshot caches that back /v1/tickers/:token/latest and /distribution.
	// endpoint ∈ {latest, distribution}; result ∈ {hit, miss}.
	// hit_ratio = sum(hit) / (sum(hit) + sum(miss)) per endpoint — alert when
	// hit_ratio drops below ~0.5 (cache too small / TTL too short / poll rate
	// pathological).
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

// OutboundOutcome maps a RoundTrip result to the closed-enum label used by
// OutboundHTTPRequestsTotal. Pass err != nil for transport-level failures
// (status is ignored in that case).
func OutboundOutcome(status int, err error) string {
	if err != nil {
		return "error"
	}
	return StatusClass(status)
}
