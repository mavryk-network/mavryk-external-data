package middleware

import (
	"net/http"
	"sync"
	"time"

	"quotes/internal/config"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimit is an inbound HTTP throttle returning 429 when the per-IP (or
// global) bucket runs out of tokens; disabled when cfg.RPS <= 0. /healthz and
// /readyz are exempt — a kubelet probe starved of tokens restarts the pod.
// Per-IP buckets live in an in-memory map, evicted once idle.
func RateLimit(cfg config.ServerRateLimitConfig) gin.HandlerFunc {
	if cfg.RPS <= 0 {
		return func(c *gin.Context) { c.Next() }
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = int(cfg.RPS*2 + 0.5)
		if burst < 1 {
			burst = 1
		}
	}

	if !cfg.PerIP {
		l := rate.NewLimiter(rate.Limit(cfg.RPS), burst)
		return func(c *gin.Context) {
			if isHealthRoute(c) {
				c.Next()
				return
			}
			if !l.Allow() {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"code":    "RATE_LIMITED",
					"message": "Too many requests",
				})
				return
			}
			c.Next()
		}
	}

	store := newIPLimiterStore(rate.Limit(cfg.RPS), burst)
	go store.evictLoop(15 * time.Minute)
	return func(c *gin.Context) {
		if isHealthRoute(c) {
			c.Next()
			return
		}
		l := store.limiter(c.ClientIP())
		if !l.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    "RATE_LIMITED",
				"message": "Too many requests",
			})
			return
		}
		c.Next()
	}
}

func isHealthRoute(c *gin.Context) bool {
	p := c.Request.URL.Path
	return p == "/healthz" || p == "/readyz"
}

type ipBucket struct {
	limiter *rate.Limiter
	lastUse time.Time
}

type ipLimiterStore struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	rps     rate.Limit
	burst   int
}

func newIPLimiterStore(rps rate.Limit, burst int) *ipLimiterStore {
	return &ipLimiterStore{
		buckets: make(map[string]*ipBucket),
		rps:     rps,
		burst:   burst,
	}
}

func (s *ipLimiterStore) limiter(ip string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if b, ok := s.buckets[ip]; ok {
		b.lastUse = now
		return b.limiter
	}
	b := &ipBucket{
		limiter: rate.NewLimiter(s.rps, s.burst),
		lastUse: now,
	}
	s.buckets[ip] = b
	return b.limiter
}

// evictLoop drops buckets unused for `idle` to bound memory. Intentionally not
// stoppable — the store lives for the process lifetime.
func (s *ipLimiterStore) evictLoop(idle time.Duration) {
	t := time.NewTicker(idle)
	defer t.Stop()
	for now := range t.C {
		s.mu.Lock()
		for ip, b := range s.buckets {
			if now.Sub(b.lastUse) > idle {
				delete(s.buckets, ip)
			}
		}
		s.mu.Unlock()
	}
}
