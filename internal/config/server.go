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
	// LatestQuoteCacheTTLSeconds: TTL for in-process cache of GET /quotes/last and GET /:token without from/to.
	// 0 disables the cache. Recommended production value: 5 (seconds).
	LatestQuoteCacheTTLSeconds int        `yaml:"latest_quote_cache_ttl_seconds"`
	CORS                       CORSConfig `yaml:"cors"`
}
