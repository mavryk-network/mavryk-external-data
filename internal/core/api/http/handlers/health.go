package handlers

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ReadinessGate makes /readyz return 503 even while the DB is alive, so a load
// balancer drains traffic before the server stops accepting.
type ReadinessGate struct {
	draining atomic.Bool
}

func NewReadinessGate() *ReadinessGate { return &ReadinessGate{} }

// StartDraining flips the gate. Idempotent.
func (g *ReadinessGate) StartDraining() { g.draining.Store(true) }

// IsDraining reports the current state.
func (g *ReadinessGate) IsDraining() bool { return g.draining.Load() }

// Liveness returns 200 as long as the process is running.
func Liveness() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "alive", "service": "mavryk-external-data"})
	}
}

// readinessPingTTL bounds DB work under a probe flood: /readyz is exempt from
// the inbound rate limiter, and 2s of staleness sits well inside any probe's
// failureThreshold budget.
const readinessPingTTL = 2 * time.Second

// cachedProbe coalesces concurrent readiness checks: the mutex is held ACROSS
// the probe, so at most one ping is in flight and waiters share its verdict.
type cachedProbe struct {
	mu      sync.Mutex
	ttl     time.Duration
	probe   func(context.Context) error
	last    time.Time
	lastErr error
}

func (p *cachedProbe) check() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.last.IsZero() && time.Since(p.last) < p.ttl {
		return p.lastErr
	}
	// Detached from the request context: a caller hanging up mid-ping must
	// not cache a spurious "unready".
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.lastErr = p.probe(ctx)
	p.last = time.Now()
	return p.lastErr
}

// Readiness reports whether the service can serve traffic: 503 while draining or
// when the DB is unreachable. The ping is capped at 2s and cached for
// readinessPingTTL so a degraded DB cannot queue up probes.
func Readiness(db *gorm.DB, gate *ReadinessGate) gin.HandlerFunc {
	ping := &cachedProbe{
		ttl: readinessPingTTL,
		probe: func(ctx context.Context) error {
			sqlDB, err := db.DB()
			if err != nil {
				return err
			}
			return sqlDB.PingContext(ctx)
		},
	}
	return func(c *gin.Context) {
		if gate != nil && gate.IsDraining() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "draining",
				"reason": "shutting_down",
			})
			return
		}
		if err := ping.check(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unready",
				"reason": "db_unreachable",
			})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	}
}
