package jobs

import (
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
)

func TestPointsTimeSpan(t *testing.T) {
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	pt := func(off time.Duration) prices.PricePoint {
		return prices.PricePoint{Timestamp: base.Add(off)}
	}

	t.Run("empty -> zero span", func(t *testing.T) {
		lo, hi := pointsTimeSpan(nil)
		if !lo.IsZero() || !hi.IsZero() {
			t.Errorf("empty span = (%v,%v), want zero,zero", lo, hi)
		}
	})

	t.Run("min and max regardless of order", func(t *testing.T) {
		pts := []prices.PricePoint{pt(2 * time.Hour), pt(-3 * time.Hour), pt(5 * time.Hour), pt(time.Hour)}
		lo, hi := pointsTimeSpan(pts)
		if !lo.Equal(base.Add(-3 * time.Hour)) {
			t.Errorf("lo = %v, want %v", lo, base.Add(-3*time.Hour))
		}
		if !hi.Equal(base.Add(5 * time.Hour)) {
			t.Errorf("hi = %v, want %v", hi, base.Add(5*time.Hour))
		}
	})

	t.Run("single point -> lo==hi (job widens hi by 1s for a valid refresh window)", func(t *testing.T) {
		lo, hi := pointsTimeSpan([]prices.PricePoint{pt(0)})
		if !lo.Equal(base) || !hi.Equal(base) {
			t.Errorf("single-point span = (%v,%v), want (%v,%v)", lo, hi, base, base)
		}
	})
}
