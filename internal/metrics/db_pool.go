package metrics

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// StartDBPoolCollector samples db.Stats() at the configured interval and
// updates the DB_pool_* gauges. Returns a stop function that the caller must
// invoke during shutdown (typically deferred from main.go).
func StartDBPoolCollector(ctx context.Context, db *gorm.DB, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		sample := func() {
			sqlDB, err := db.DB()
			if err != nil {
				return
			}
			s := sqlDB.Stats()
			DBOpenConnections.Set(float64(s.OpenConnections))
			DBInUseConnections.Set(float64(s.InUse))
			DBIdleConnections.Set(float64(s.Idle))
			DBWaitDurationSeconds.Set(s.WaitDuration.Seconds())
		}
		sample()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				sample()
			}
		}
	}()
	return cancel
}
