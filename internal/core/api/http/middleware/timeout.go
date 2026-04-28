package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
)

// HandlerTimeout caps total time spent in a handler. Cancelling the context lets
// downstream operations (DB queries, outbound HTTP) abort early when the client
// disappears or simply when the handler exceeds its budget.
//
// Note: this does NOT enforce hard deadlines on the response — gin/net/http will
// still try to write whatever the handler produced. ReadTimeout/WriteTimeout on
// the http.Server remain the wire-level cutoffs.
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
