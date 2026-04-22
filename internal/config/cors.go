package config

import "strings"

// CORSConfig controls Access-Control-* response headers (explicit origins only; no wildcard).
type CORSConfig struct {
	// AllowedOrigins is an exact-match list for the browser Origin header (e.g. https://app.example.com).
	AllowedOrigins []string `yaml:"allowed_origins"`
	// AllowedMethods defaults to GET, HEAD, OPTIONS when empty.
	AllowedMethods string `yaml:"allowed_methods"`
	// AllowedHeaders defaults to Origin, Content-Type, Accept when empty.
	AllowedHeaders string `yaml:"allowed_headers"`
}

// MatchOrigin returns requestOrigin if it is listed in AllowedOrigins (trimmed, exact match), else "".
func (c CORSConfig) MatchOrigin(requestOrigin string) string {
	requestOrigin = strings.TrimSpace(requestOrigin)
	if requestOrigin == "" {
		return ""
	}
	for _, o := range c.AllowedOrigins {
		if strings.TrimSpace(o) == requestOrigin {
			return requestOrigin
		}
	}
	return ""
}
