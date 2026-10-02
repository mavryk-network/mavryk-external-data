package config

import "strings"

// ServerConfig holds HTTP server bind options and response-cache settings.
type ServerConfig struct {
	Port string `yaml:"port"`
	// Second listener for intra-cluster traffic: same routes without the MBIO JWT
	// middleware, plus /metrics. Empty disables it (single-port mode, local dev).
	InternalPort string `yaml:"internal_port"`
	Host         string `yaml:"host"`
	// debug, release, or test. Empty derives from the host (localhost → debug).
	GinMode string `yaml:"gin_mode"`
	// net/http.Server timeouts, as Go duration strings ("30s").
	ReadTimeout       DurationYAML `yaml:"read_timeout"`
	WriteTimeout      DurationYAML `yaml:"write_timeout"`
	ReadHeaderTimeout DurationYAML `yaml:"read_header_timeout"`
	IdleTimeout       DurationYAML `yaml:"idle_timeout"`
	// Seconds; 0 disables the cache. Production: 5.
	LatestQuoteCacheTTLSeconds int `yaml:"latest_quote_cache_ttl_seconds"`
	// Hard upper bound on `?limit=`. 0 = in-code default (10000); negative rejected.
	MaxQueryLimit int `yaml:"max_query_limit"`
	// Mounts net/http/pprof on the server bind — not for a public listener.
	PprofEnabled bool `yaml:"pprof_enabled"`
	// Seconds /readyz returns 503 before HTTP shutdown starts. 0 skips the drain.
	ShutdownDrainSeconds int `yaml:"shutdown_drain_seconds"`
	// Per-request ctx timeout. 0 selects the 10s default; a NEGATIVE value
	// disables the budget (env overrides run before defaults, so
	// SERVER_HANDLER_TIMEOUT=0s still yields the default).
	HandlerTimeout DurationYAML `yaml:"handler_timeout"`
	// Applies to every route except /healthz and /readyz.
	RateLimit ServerRateLimitConfig `yaml:"rate_limit"`
	// LB/proxy CIDRs (or IPs) whose X-Forwarded-For ClientIP() honors. Empty
	// trusts no proxy, which behind an LB collapses every client into one
	// rate-limit bucket.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// Age at which a CoinGecko FX rate is tagged `fx.stale=true` but still
	// served; refusal happens only past prices.FXHardStalenessCap (26h), which
	// Validate requires this to stay below. 0 = in-code default (300s).
	FXMaxStalenessSeconds int `yaml:"fx_max_staleness_seconds"`
	// Cap on comma-separated currencies per `?in=`. 0 = in-code default (10).
	MaxInCurrencies int `yaml:"max_in_currencies"`
	// /v1/tickers/:token/latest hides rows older than this unless
	// `?include_stale=true`; /distribution always excludes them.
	// 0 = in-code default (1h).
	TickerStaleAfter DurationYAML `yaml:"ticker_stale_after"`
}

// EffectiveGinMode resolves GinMode to the mode the HTTP layer actually runs:
// explicit debug/release/test win; empty derives from the bind host
// (localhost → debug, else release); unknown → release. Load writes the result
// back so every consumer agrees on what "production" means.
func (s ServerConfig) EffectiveGinMode() string {
	switch strings.ToLower(strings.TrimSpace(s.GinMode)) {
	case "debug":
		return "debug"
	case "test":
		return "test"
	case "release":
		return "release"
	case "":
		h := strings.TrimSpace(s.Host)
		if h == "localhost" || h == "127.0.0.1" {
			return "debug"
		}
		return "release"
	default:
		return "release"
	}
}

// ServerRateLimitConfig controls inbound HTTP throttling. Disabled when RPS == 0;
// PerIP buckets per ClientIP, otherwise all callers share one bucket.
type ServerRateLimitConfig struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
	PerIP bool    `yaml:"per_ip"`
}
