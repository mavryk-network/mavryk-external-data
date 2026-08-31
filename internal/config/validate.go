package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	apiprices "quotes/internal/core/application/prices"
)

// Validate checks required fields and formats after defaults and env overrides
// are applied. Token-existence checks happen later, after the runtime token
// registry is loaded from DB.
//
// Implementation note: each section delegates to a focused validator so that
// (a) gocyclo stays sane and (b) tests can drive sections in isolation.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	for _, fn := range []func() error{
		c.validateServer,
		c.validateDatabase,
		c.validateJob,
		c.validateAPI,
		c.validateCoinGecko,
		c.validateBackfill,
		c.validateEquiteezBackfill,
		c.validateEquiteez,
		c.validateTokens,
		c.validateRWA,
		c.validateTickers,
		c.validateAuth,
		c.validateProductionSafety,
	} {
		if err := fn(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateServer() error {
	if strings.TrimSpace(c.Server.Host) == "" {
		return fmt.Errorf("server.host is required")
	}
	if strings.TrimSpace(c.Server.Port) == "" {
		return fmt.Errorf("server.port is required")
	}
	if port, err := strconv.Atoi(strings.TrimSpace(c.Server.Port)); err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("server.port must be a TCP port number 1-65535, got %q", c.Server.Port)
	}
	if ip := strings.TrimSpace(c.Server.InternalPort); ip != "" {
		port, err := strconv.Atoi(ip)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("server.internal_port must be a TCP port number 1-65535, got %q", c.Server.InternalPort)
		}
		if ip == strings.TrimSpace(c.Server.Port) {
			return fmt.Errorf("server.internal_port (%q) must differ from server.port", ip)
		}
	}
	if c.Server.LatestQuoteCacheTTLSeconds < 0 {
		return fmt.Errorf("server.latest_quote_cache_ttl_seconds must be >= 0, got %d", c.Server.LatestQuoteCacheTTLSeconds)
	}
	if c.Server.MaxQueryLimit < 0 {
		return fmt.Errorf("server.max_query_limit must be >= 0, got %d", c.Server.MaxQueryLimit)
	}
	if c.Server.FXMaxStalenessSeconds < 0 {
		return fmt.Errorf("server.fx_max_staleness_seconds must be >= 0, got %d", c.Server.FXMaxStalenessSeconds)
	}
	// The soft budget only tags a rate stale; the hard cap refuses it outright.
	// A budget at or above the cap inverts them: every rate the budget meant to
	// serve flagged stale is refused first, so `fx.stale` becomes unreachable
	// and the knob silently does nothing. Refuse that config instead.
	if hardCap := int(apiprices.FXHardStalenessCap / time.Second); c.Server.FXMaxStalenessSeconds >= hardCap {
		return fmt.Errorf(
			"server.fx_max_staleness_seconds must be < %d (the hard staleness cap past which conversions are refused), got %d",
			hardCap, c.Server.FXMaxStalenessSeconds)
	}
	if c.Server.MaxInCurrencies < 0 {
		return fmt.Errorf("server.max_in_currencies must be >= 0, got %d", c.Server.MaxInCurrencies)
	}
	if c.Server.TickerStaleAfter < 0 {
		return fmt.Errorf("server.ticker_stale_after must be >= 0, got %s", time.Duration(c.Server.TickerStaleAfter))
	}
	if err := validatePositiveDuration("server.read_timeout", c.Server.ReadTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("server.write_timeout", c.Server.WriteTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("server.read_header_timeout", c.Server.ReadHeaderTimeout); err != nil {
		return err
	}
	if err := validatePositiveDuration("server.idle_timeout", c.Server.IdleTimeout); err != nil {
		return err
	}
	if err := validateGinMode(c.Server.GinMode); err != nil {
		return err
	}
	if c.Server.RateLimit.RPS < 0 {
		return fmt.Errorf("server.rate_limit.rps must be >= 0")
	}
	if c.Server.RateLimit.Burst < 0 {
		return fmt.Errorf("server.rate_limit.burst must be >= 0")
	}
	for _, p := range c.Server.TrustedProxies {
		p = strings.TrimSpace(p)
		if _, _, err := net.ParseCIDR(p); err == nil {
			continue
		}
		if net.ParseIP(p) != nil {
			continue
		}
		return fmt.Errorf("server.trusted_proxies entry %q is neither a CIDR nor an IP", p)
	}
	return nil
}

func (c *Config) validateDatabase() error {
	if strings.TrimSpace(c.Database.Host) == "" {
		return fmt.Errorf("database.host is required")
	}
	if strings.TrimSpace(c.Database.Port) == "" {
		return fmt.Errorf("database.port is required")
	}
	if dbPort, err := strconv.Atoi(strings.TrimSpace(c.Database.Port)); err != nil || dbPort < 1 || dbPort > 65535 {
		return fmt.Errorf("database.port must be a TCP port number 1-65535, got %q", c.Database.Port)
	}
	if strings.TrimSpace(c.Database.User) == "" {
		return fmt.Errorf("database.user is required")
	}
	if strings.TrimSpace(c.Database.Name) == "" {
		return fmt.Errorf("database.name is required")
	}
	if c.Database.MaxOpenConns < 0 {
		return fmt.Errorf("database.max_open_conns must be >= 0")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("database.max_idle_conns must be >= 0")
	}
	if c.Database.MaxOpenConns > 0 && c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("database.max_idle_conns (%d) must be <= max_open_conns (%d)",
			c.Database.MaxIdleConns, c.Database.MaxOpenConns)
	}
	return nil
}

func (c *Config) validateJob() error {
	if c.Job.Enabled && c.Job.IntervalSeconds <= 0 {
		return fmt.Errorf("job.interval_seconds must be > 0 when job.enabled is true")
	}
	return nil
}

func (c *Config) validateAPI() error {
	if c.API.TimeoutSeconds <= 0 {
		return fmt.Errorf("api.timeout_seconds must be > 0")
	}
	return nil
}

func (c *Config) validateCoinGecko() error {
	if strings.TrimSpace(c.CoinGecko.BaseURL) == "" {
		return fmt.Errorf("coingecko.base_url is required")
	}
	if err := requireSecureUpstreamBase("coingecko.base_url", c.CoinGecko.BaseURL); err != nil {
		return err
	}
	if base := strings.TrimSpace(c.Equiteez.IndexerURL); base != "" {
		if err := requireSecureUpstreamBase("equiteez.indexer_url", base); err != nil {
			return err
		}
	}
	if err := validateRateLimit("coingecko", c.CoinGecko.RateLimit); err != nil {
		return err
	}
	return validateRateLimit("equiteez", c.Equiteez.RateLimit)
}

// requireSecureUpstreamBase rejects a non-https upstream base URL unless it
// targets localhost (local dev / testcontainers). API keys and the Equiteez
// bypass secret ride on these requests; an http hop hands them to any on-path
// attacker.
func requireSecureUpstreamBase(name, base string) error {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %w", name, err)
	}
	host := u.Hostname()
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !isLocal {
		return fmt.Errorf("%s must use https (got %q); credentials ride on these requests and an http hop exposes them", name, u.Scheme)
	}
	return nil
}

func (c *Config) validateBackfill() error {
	b := c.Backfill
	if b.TickSeconds < 0 {
		return fmt.Errorf("backfill.tick_seconds must be >= 0")
	}
	if b.ChunkMinutes < 0 {
		return fmt.Errorf("backfill.chunk_minutes must be >= 0")
	}
	// CoinGecko market_chart/range granularity drops to 1h once the window exceeds
	// ~1 day; we keep chunks <= 24h to preserve 5-min points for the backfill pass.
	if b.ChunkMinutes > 1440 {
		return fmt.Errorf("backfill.chunk_minutes must be <= 1440 (24h) to keep CoinGecko 5-min granularity, got %d", b.ChunkMinutes)
	}
	if b.BackfillMaxErrors < 0 {
		return fmt.Errorf("backfill.backfill_max_errors must be >= 0")
	}
	if b.BackoffInitialMs < 0 {
		return fmt.Errorf("backfill.backoff_initial_ms must be >= 0")
	}
	if b.BackoffMaxMs < 0 {
		return fmt.Errorf("backfill.backoff_max_ms must be >= 0")
	}
	if b.MaxBackoffMs > 0 && b.MaxBackoffMs < b.BackoffMaxMs {
		return fmt.Errorf("backfill.max_backoff_ms (hard cap) must be >= backoff_max_ms")
	}
	if err := validateBackfillStartFrom(b.StartFrom); err != nil {
		return fmt.Errorf("backfill.start_from: %w", err)
	}
	if err := validateBackfillStartFrom(b.MinStartFrom); err != nil {
		return fmt.Errorf("backfill.min_start_from: %w", err)
	}
	return nil
}

func (c *Config) validateTokens() error {
	for name, tc := range c.Tokens {
		if err := validateOneToken(name, tc); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateEquiteezBackfill() error {
	b := c.Equiteez.Backfill
	if !b.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Equiteez.IndexerURL) == "" {
		return fmt.Errorf("equiteez.backfill.enabled=true requires equiteez.indexer_url to be set")
	}
	if b.TickSeconds <= 0 {
		return fmt.Errorf("equiteez.backfill.tick_seconds must be > 0 when enabled")
	}
	if b.BatchSize <= 0 {
		return fmt.Errorf("equiteez.backfill.batch_size must be > 0 when enabled")
	}
	if b.BatchSize > 5000 {
		// Hasura's default node limit + our outbound max-bytes guard sit far below this;
		// pulling 5k orders in one shot is asking for OOM/timeout pain.
		return fmt.Errorf("equiteez.backfill.batch_size must be <= 5000, got %d", b.BatchSize)
	}
	if b.JitterMs < 0 {
		return fmt.Errorf("equiteez.backfill.jitter_ms must be >= 0")
	}
	if b.BackfillMaxErrors < 0 {
		return fmt.Errorf("equiteez.backfill.backfill_max_errors must be >= 0")
	}
	if b.BackoffInitialMs < 0 {
		return fmt.Errorf("equiteez.backfill.backoff_initial_ms must be >= 0")
	}
	if b.BackoffMaxMs < 0 {
		return fmt.Errorf("equiteez.backfill.backoff_max_ms must be >= 0")
	}
	if b.MaxBackoffMs > 0 && b.MaxBackoffMs < b.BackoffMaxMs {
		return fmt.Errorf("equiteez.backfill.max_backoff_ms (hard cap) must be >= backoff_max_ms")
	}
	if err := validateBackfillStartFrom(b.StartFrom); err != nil {
		return fmt.Errorf("equiteez.backfill.start_from: %w", err)
	}
	return nil
}

// validateEquiteez checks the Equiteez indexer connection settings that apply
// beyond backfill (the sync and live jobs use the same client). When a bypass
// password is configured, the indexer URL must parse — otherwise
// indexerRequestURL silently drops the credential and every GraphQL call fails
// unauthenticated with a misleading error.
func (c *Config) validateEquiteez() error {
	if strings.TrimSpace(c.Equiteez.IndexerPassword) == "" {
		return nil
	}
	raw := strings.TrimSpace(c.Equiteez.IndexerURL)
	if raw == "" {
		return fmt.Errorf("equiteez.indexer_password is set but equiteez.indexer_url is empty")
	}
	if _, err := url.Parse(raw); err != nil {
		return fmt.Errorf("equiteez.indexer_url must be a valid URL when a bypass password is set: %w", err)
	}
	return nil
}

func (c *Config) validateTickers() error {
	t := c.Tickers
	if !t.Enabled {
		return nil
	}
	if strings.TrimSpace(t.TokenSymbol) == "" {
		return fmt.Errorf("tickers.token_symbol is required when tickers.enabled is true")
	}
	if t.IntervalSeconds <= 0 {
		return fmt.Errorf("tickers.interval_seconds must be > 0 when tickers.enabled is true, got %d", t.IntervalSeconds)
	}
	if t.HTTPTimeout <= 0 {
		return fmt.Errorf("tickers.http_timeout must be > 0 when tickers.enabled is true, got %s", time.Duration(t.HTTPTimeout))
	}
	if t.Cache.LatestTTLSeconds < 0 {
		return fmt.Errorf("tickers.cache.latest_ttl_seconds must be >= 0")
	}
	if t.Cache.DistributionTTLSeconds < 0 {
		return fmt.Errorf("tickers.cache.distribution_ttl_seconds must be >= 0")
	}
	return nil
}

func (c *Config) validateRWA() error {
	if c.RWA.IntervalSeconds < 0 {
		return fmt.Errorf("rwa.interval_seconds must be >= 0")
	}
	if c.RWA.Enabled && c.RWA.IntervalSeconds <= 0 {
		return fmt.Errorf("rwa.interval_seconds must be > 0 when rwa.enabled is true")
	}
	if c.RWA.PairSyncIntervalSeconds < 0 {
		return fmt.Errorf("rwa.pair_sync_interval_seconds must be >= 0")
	}
	if c.RWA.Enabled && strings.TrimSpace(c.Equiteez.IndexerURL) == "" {
		return fmt.Errorf("rwa.enabled=true requires equiteez.indexer_url to be set")
	}
	return nil
}

// validateAuth checks the MBIO JWT settings when verification is enabled.
// Verification is on by default; explicit `auth.enabled: false` disables all checks.
//
// Matches rwa-backend's posture: audience is **optional**. MBIO currently mints
// tokens with only iss/sub/exp/iat (no aud), so enforcing aud would reject every
// real token. The middleware emits a one-shot startup warn-log when aud is empty
// so the missing check stays visible in ops logs.
func (c *Config) validateAuth() error {
	a := &c.Auth
	if !a.JWTVerificationEnabled() {
		return nil
	}
	if strings.TrimSpace(a.MBIOJWTIssuer) == "" {
		return fmt.Errorf("auth.mbio_jwt_issuer is required when auth is enabled (env AUTH_MBIO_JWT_ISSUER)")
	}
	if a.JWKSCacheTTL < 0 {
		return fmt.Errorf("auth.jwks_cache_ttl must be >= 0, got %s", a.JWKSCacheTTL)
	}
	if a.LocalJWTVerifyConfigured() {
		// Local-key mode swaps the MBIO JWKS trust anchor for whatever key the
		// env carries — a leaked CI var reaching prod would silently accept
		// attacker-minted tokens. Dev/CI only; refuse it in effective release.
		if c.Server.EffectiveGinMode() == "release" {
			return fmt.Errorf("auth.jwt_local_verify_public_key (AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY) is set while the effective gin mode is release; " +
				"local-key verification bypasses the MBIO JWKS trust anchor and is dev/CI-only — unset it or set SERVER_GIN_MODE=debug for dev")
		}
		pemBytes, err := a.LocalJWTVerifyPublicKeyPEMBytes()
		if err != nil {
			return err
		}
		if len(pemBytes) == 0 {
			return fmt.Errorf("auth.jwt_local_verify_public_key is empty after base64 decode (AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY)")
		}
		// Deeper PEM validity + the 2048-bit floor (ParseRSAPublicKeyFromPEM)
		// happen at middleware build time — keeps validate.go free of the JWT dep.
		return nil
	}
	base := strings.TrimSpace(a.MBIOJWTBaseURL)
	if base == "" {
		base = strings.TrimSpace(a.MBIOAPIGatewayBaseURL)
	}
	if base == "" {
		return fmt.Errorf("auth.mbio_jwt_base_url or auth.mbio_api_gateway_base_url is required when AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY is unset; set one of AUTH_MBIO_JWT_BASE_URL / AUTH_MBIO_API_GATEWAY_BASE_URL, or provide a base64 PEM in AUTH_JWT_LOCAL_VERIFY_PUBLIC_KEY for local RS256 verification")
	}
	// JWKS (signing keys) must be fetched over TLS: an http:// base lets an
	// on-path attacker substitute their own key set and forge accepted tokens —
	// a full auth bypass. Allow http only for explicit localhost dev.
	if err := requireSecureJWKSBase(base); err != nil {
		return err
	}
	return nil
}

// requireSecureJWKSBase rejects a non-https JWKS base URL unless it targets
// localhost/127.0.0.1 (local dev).
func requireSecureJWKSBase(base string) error {
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("auth JWKS base URL is not a valid URL: %w", err)
	}
	host := u.Hostname()
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if u.Scheme != "https" && !isLocal {
		return fmt.Errorf("auth JWKS base URL must use https (got %q); an http endpoint allows MITM key injection and full auth bypass", u.Scheme)
	}
	return nil
}

// validateProductionSafety refuses to start in release mode with well-known
// default credentials or with RWA auth disabled. Caught at config-load so the
// operator sees a clear error instead of a deploy that silently leaks behind a
// default password or serves RWA data on the public listener with no token.
//
// Gates on the EFFECTIVE gin mode: an empty gin_mode on a non-localhost host
// runs release at the HTTP layer, so it must be treated as production here too.
func (c *Config) validateProductionSafety() error {
	if c.Server.EffectiveGinMode() != "release" {
		return nil
	}
	// Auth off on the public listener in release mode exposes every /v1/rwa/*
	// and /v1/pairs/rwa route without a token. Disabling auth is a dev/CI-only
	// convenience (AUTH_ENABLED=false); refuse it in effective release mode.
	if !c.Auth.JWTVerificationEnabled() {
		return fmt.Errorf("auth is disabled (auth.enabled=false / AUTH_ENABLED=false) while the effective gin mode is release; " +
			"RWA routes would be served unauthenticated on the public listener. Enable auth (unset AUTH_ENABLED or set it true) " +
			"or set SERVER_GIN_MODE=debug for dev/CI")
	}
	insecure := map[string]bool{
		"postgres": true,
		"admin":    true,
		"password": true,
		"changeme": true,
		"qwerty":   true, // Makefile's local default
	}
	if insecure[strings.ToLower(strings.TrimSpace(c.Database.Password))] {
		return fmt.Errorf("database.password is a well-known default (%q); refusing to start in release mode",
			c.Database.Password)
	}
	return nil
}

// --- helpers (each kept under cyclomatic complexity budget) ---

func validatePositiveDuration(name string, d DurationYAML) error {
	if d <= 0 {
		return fmt.Errorf("%s must be > 0, got %s", name, time.Duration(d))
	}
	return nil
}

func validateGinMode(mode string) error {
	gm := strings.ToLower(strings.TrimSpace(mode))
	if gm == "" {
		return nil
	}
	switch gm {
	case "debug", "release", "test":
		return nil
	default:
		return fmt.Errorf("server.gin_mode must be one of debug, release, test, or empty, got %q", mode)
	}
}

func validateOneToken(name string, tc TokenConfig) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tokens: empty token key is not allowed")
	}
	if err := validateBackfillStartFrom(tc.Backfill.StartFrom); err != nil {
		return fmt.Errorf("tokens[%s].backfill.start_from: %w", name, err)
	}
	if tc.Backfill.ChunkMinutes < 0 {
		return fmt.Errorf("tokens[%s].backfill.chunk_minutes must be >= 0", name)
	}
	if tc.Backfill.ChunkMinutes > 1440 {
		return fmt.Errorf("tokens[%s].backfill.chunk_minutes must be <= 1440 (24h), got %d", name, tc.Backfill.ChunkMinutes)
	}
	if tc.LiveLookbackSeconds < 0 {
		return fmt.Errorf("tokens[%s].live_lookback_seconds must be >= 0", name)
	}
	if tc.IntervalSeconds < 0 {
		return fmt.Errorf("tokens[%s].interval_seconds must be >= 0", name)
	}
	if tc.MaxChunkMinutes > 0 && tc.Backfill.ChunkMinutes > tc.MaxChunkMinutes {
		return fmt.Errorf("tokens[%s]: backfill.chunk_minutes (%d) must be <= max_chunk_minutes (%d)",
			name, tc.Backfill.ChunkMinutes, tc.MaxChunkMinutes)
	}
	return nil
}

func validateRateLimit(section string, r RateLimitConfig) error {
	if r.RPS < 0 {
		return fmt.Errorf("%s.rate_limit.rps must be >= 0, got %v", section, r.RPS)
	}
	if r.Burst < 0 {
		return fmt.Errorf("%s.rate_limit.burst must be >= 0, got %d", section, r.Burst)
	}
	return nil
}

func validateBackfillStartFrom(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return nil
	}
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return nil
	}
	return fmt.Errorf("must be RFC3339 (e.g. 2025-01-15T00:00:00Z) or date YYYY-MM-DD, got %q", s)
}
