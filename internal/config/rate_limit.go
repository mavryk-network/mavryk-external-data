package config

import "quotes/internal/core/infrastructure/httpclient"

// RateLimitConfig is a reusable per-service outbound rate-limit definition.
// Embed it in any service config (CoinGecko, Equiteez, ...) that talks HTTP to a
// third party. Zero RPS disables the limiter for that component.
//
// Burst defaults to 2×RPS at build time when left at 0; that's enough to smooth
// bursty callers (e.g. a validator-batch kickoff) without exceeding the upstream
// quota measured in rpm.
type RateLimitConfig struct {
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
}

// Settings materializes httpclient.RateLimitSettings for the given component name.
// The component name drives the shared-limiter registry in httpclient: every
// caller passing the same component shares one token bucket process-wide.
func (r RateLimitConfig) Settings(component string) httpclient.RateLimitSettings {
	if r.RPS <= 0 {
		return httpclient.RateLimitSettings{Component: component}
	}
	burst := r.Burst
	if burst <= 0 {
		burst = int(r.RPS*2 + 0.5)
		if burst < 1 {
			burst = 1
		}
	}
	return httpclient.RateLimitSettings{
		Component: component,
		RPS:       r.RPS,
		Burst:     burst,
	}
}
