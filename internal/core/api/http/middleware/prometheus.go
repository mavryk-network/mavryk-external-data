package middleware

import (
	"time"

	"quotes/internal/metrics"

	"github.com/gin-gonic/gin"
)

// PrometheusHTTP records http_* metrics using Gin route templates (FullPath).
func PrometheusHTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unknown"
		}
		method := c.Request.Method
		status := c.Writer.Status()

		metrics.HTTPRequestsTotal.WithLabelValues(method, path).Inc()
		metrics.HTTPResponsesTotal.WithLabelValues(method, path, metrics.StatusClass(status)).Inc()
		metrics.HTTPRequestDurationSeconds.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
	}
}
