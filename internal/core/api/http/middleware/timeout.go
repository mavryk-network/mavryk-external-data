package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// HandlerTimeout caps time spent in a handler by cancelling the request context,
// letting DB queries and outbound HTTP abort early. It does NOT bound the
// response write — ReadTimeout/WriteTimeout stay the wire-level cutoffs.
func HandlerTimeout(d time.Duration) gin.HandlerFunc {
	if d <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}
