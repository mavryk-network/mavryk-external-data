package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// HandlerTimeout is default-on for every request: a positive budget must
// expire the request context, a non-positive one must leave no deadline.
func TestHandlerTimeout_ExpiresRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(HandlerTimeout(50 * time.Millisecond))
	expired := make(chan bool, 1)
	r.GET("/slow", func(c *gin.Context) {
		select {
		case <-c.Request.Context().Done():
			expired <- true
		case <-time.After(2 * time.Second):
			expired <- false
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/slow", nil))
	if !<-expired {
		t.Fatal("50ms budget did not cancel the request context")
	}
}

func TestHandlerTimeout_NonPositiveLeavesNoDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, d := range []time.Duration{0, -1 * time.Second} {
		r := gin.New()
		r.Use(HandlerTimeout(d))
		var hadDeadline bool
		r.GET("/x", func(c *gin.Context) {
			_, hadDeadline = c.Request.Context().Deadline()
			c.Status(http.StatusOK)
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if hadDeadline {
			t.Fatalf("HandlerTimeout(%v) must not set a deadline", d)
		}
	}
}
