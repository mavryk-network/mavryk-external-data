package config

// ServerConfig holds HTTP server bind options and response-cache settings.
type ServerConfig struct {
	Port string `yaml:"port"`
	Host string `yaml:"host"`
	// GinMode: debug, release, or test. Empty means derive from host (localhost/127.0.0.1 → debug).
	GinMode string `yaml:"gin_mode"`
	// HTTP server timeouts (see net/http.Server). YAML uses Go duration strings, e.g. "30s".
	ReadTimeout       DurationYAML `yaml:"read_timeout"`
	WriteTimeout      DurationYAML `yaml:"write_timeout"`
	ReadHeaderTimeout DurationYAML `yaml:"read_header_timeout"`
	IdleTimeout       DurationYAML `yaml:"idle_timeout"`
	// LatestQuoteCacheTTLSeconds: TTL for the in-process snapshot cache (latest queries).
	// 0 disables the cache. Recommended production value: 5 (seconds).
	LatestQuoteCacheTTLSeconds int `yaml:"latest_quote_cache_ttl_seconds"`
	// MaxQueryLimit: hard upper bound on the `?limit=` parameter for list endpoints.
	// 0 means use the in-code default (10000). Negative is rejected by Validate.
	MaxQueryLimit int `yaml:"max_query_limit"`
	// PprofEnabled exposes net/http/pprof on the server bind. Off by default.
	PprofEnabled bool `yaml:"pprof_enabled"`
	// ShutdownDrainSeconds — once shutdown begins, /readyz returns 503 for this
	// many seconds before HTTP shutdown starts. 0 disables the drain phase.
	ShutdownDrainSeconds int `yaml:"shutdown_drain_seconds"`
	// HandlerTimeout caps total time inside a handler (per-request ctx timeout).
	// 0 means "no per-handler limit; rely on Server.{Read,Write}Timeout".
	HandlerTimeout DurationYAML `yaml:"handler_timeout"`
	// RateLimit is an optional inbound rate limit applied to all routes.
	RateLimit ServerRateLimitConfig `yaml:"rate_limit"`
	// FXMaxStalenessSeconds — how old a CoinGecko FX rate may be before
	// `?in=` responses tag it `fx.stale=true` (still served, never blocks).
	// 0 means use the in-code default (300s).
	FXMaxStalenessSeconds int `yaml:"fx_max_staleness_seconds"`
	// MaxInCurrencies — cap on the number of comma-separated currencies a
	// single request may pass to `?in=`. Defends against `?in=usd,eur,...`
	// spam. 0 means use the in-code default (10).
	MaxInCurrencies int        `yaml:"max_in_currencies"`
	CORS            CORSConfig `yaml:"cors"`
}

// ServerRateLimitConfig controls inbound HTTP throttling.
//
// Disabled when RPS == 0 (default). When PerIP=true the bucket is per ClientIP;
// otherwise one bucket is shared by all callers (cheap fallback for closed
// internal services).
type ServerRateLimitConfig struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
	PerIP bool    `yaml:"per_ip"`
}
