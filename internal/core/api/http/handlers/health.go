package handlers

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ReadinessGate is an atomic flag flipped by the lifecycle code: when set, the
// /readyz handler returns 503 even if the DB is alive. Used during graceful
// shutdown so the load balancer drains traffic before HTTP server stops accepting.
type ReadinessGate struct {
	draining atomic.Bool
}

func NewReadinessGate() *ReadinessGate { return &ReadinessGate{} }

// StartDraining flips the gate. Idempotent.
func (g *ReadinessGate) StartDraining() { g.draining.Store(true) }

// IsDraining reports the current state.
func (g *ReadinessGate) IsDraining() bool { return g.draining.Load() }

// Liveness returns 200 as long as the process is running. K8s liveness probe.
func Liveness() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "alive", "service": "mavryk-external-data"})
	}
}

// Readiness reports whether the service can serve traffic. Returns 503 when
// either the readiness gate is "draining" (during graceful shutdown) or the DB
// is unreachable. Future: data-freshness check, circuit-breaker state.
//
// A 2s timeout caps probe latency so a degraded DB doesn't queue up readiness
// requests behind a slow ping.
func Readiness(db *gorm.DB, gate *ReadinessGate) gin.HandlerFunc {
	return func(c *gin.Context) {
		if gate != nil && gate.IsDraining() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "draining",
				"reason": "shutting_down",
			})
			return
		}
		sqlDB, err := db.DB()
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unready",
				"reason": "db_handle_unavailable",
			})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := sqlDB.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unready",
				"reason": "db_unreachable",
			})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	}
}
