package middleware

import (
	"strings"

	"quotes/internal/config"

	"github.com/gin-gonic/gin"
)

// CORS sets Access-Control-* headers from config (explicit Origin allowlist; echoes matched Origin only).
func CORS(cfg config.CORSConfig) gin.HandlerFunc {
	methods := strings.TrimSpace(cfg.AllowedMethods)
	if methods == "" {
		methods = "GET, HEAD, OPTIONS"
	}
	headers := strings.TrimSpace(cfg.AllowedHeaders)
	if headers == "" {
		headers = "Origin, Content-Type, Accept"
	}
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin != "" {
			if allowed := cfg.MatchOrigin(origin); allowed != "" {
				c.Header("Access-Control-Allow-Origin", allowed)
				c.Header("Vary", "Origin")
			}
		}
		c.Header("Access-Control-Allow-Methods", methods)
		c.Header("Access-Control-Allow-Headers", headers)

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
