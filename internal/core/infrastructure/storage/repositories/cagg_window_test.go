package repositories

import (
	"testing"
	"time"
)

func TestViewBucketWidth(t *testing.T) {
	cases := map[string]time.Duration{
		"token_prices_1m":     time.Minute,
		"token_prices_1h":     time.Hour,
		"token_prices_1d":     24 * time.Hour,
		"rwa_quote_prices_1m": time.Minute,
		"rwa_quote_prices_1h": time.Hour,
		"rwa_quote_prices_1d": 24 * time.Hour,
		"something_else":      0,
	}
	for view, want := range cases {
		if got := viewBucketWidth(view); got != want {
			t.Errorf("viewBucketWidth(%q) = %v, want %v", view, got, want)
		}
	}
}

// The widened window must fully enclose every bucket it touches, so
// refresh_continuous_aggregate never errors "refresh window too small".
func TestAlignToBucket(t *testing.T) {
	day := 24 * time.Hour
	mid := time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)

	t.Run("6h chunk widens to the full day", func(t *testing.T) {
		wf, wt := alignToBucket(mid, mid.Add(6*time.Hour), day)
		if !wf.Equal(time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("wf = %v", wf)
		}
		if !wt.Equal(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("wt = %v", wt)
		}
	})

	t.Run("boundary-aligned window is untouched", func(t *testing.T) {
		from := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
		to := from.Add(2 * day)
		wf, wt := alignToBucket(from, to, day)
		if !wf.Equal(from) || !wt.Equal(to) {
			t.Errorf("aligned window changed: [%v, %v]", wf, wt)
		}
	})

	t.Run("cross-midnight chunk covers both days", func(t *testing.T) {
		from := time.Date(2026, 3, 14, 22, 0, 0, 0, time.UTC)
		wf, wt := alignToBucket(from, from.Add(6*time.Hour), day)
		if !wf.Equal(time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)) ||
			!wt.Equal(time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("window = [%v, %v]", wf, wt)
		}
	})

	t.Run("degenerate equal bounds still yield one bucket", func(t *testing.T) {
		wf, wt := alignToBucket(mid, mid, time.Hour)
		if wt.Sub(wf) != time.Hour {
			t.Errorf("window = [%v, %v], want exactly one whole bucket", wf, wt)
		}
	})

	t.Run("zero width passes through", func(t *testing.T) {
		wf, wt := alignToBucket(mid, mid.Add(time.Hour), 0)
		if !wf.Equal(mid) || !wt.Equal(mid.Add(time.Hour)) {
			t.Errorf("window = [%v, %v]", wf, wt)
		}
	})
}
