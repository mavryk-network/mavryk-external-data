package jobs

import (
	"errors"
	"testing"
	"time"
)

func TestComputeBackoff_ExponentialAndCaps(t *testing.T) {
	// initial=2000, max=60_000, hardCap=24h
	cases := []struct {
		errorCount int
		want       time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 60 * time.Second}, // hits maxMs
		{99, 60 * time.Second},
	}
	for _, c := range cases {
		got := computeBackoff(c.errorCount, 2000, 60000, 24*60*60*1000)
		if got != c.want {
			t.Errorf("errorCount=%d: got %v, want %v", c.errorCount, got, c.want)
		}
	}
}

func TestComputeBackoff_Minimum1Second(t *testing.T) {
	got := computeBackoff(1, 100, 500, 1000)
	if got < time.Second {
		t.Errorf("expected min 1s; got %v", got)
	}
}

func TestComputeBackoff_HardCap(t *testing.T) {
	// MaxMs=10h but hardCap=1h → result should clamp to 1h.
	got := computeBackoff(50, 1000, int((10 * time.Hour).Milliseconds()), int((1 * time.Hour).Milliseconds()))
	if got != time.Hour {
		t.Errorf("hard cap must clamp: got %v, want 1h", got)
	}
}

func TestParseBackfillTime_Formats(t *testing.T) {
	if _, err := parseBackfillTime("2025-09-18T14:00:00Z"); err != nil {
		t.Errorf("RFC3339: %v", err)
	}
	if _, err := parseBackfillTime("2025-09-18"); err != nil {
		t.Errorf("YYYY-MM-DD: %v", err)
	}
	if t0, err := parseBackfillTime("   "); err != nil || !t0.IsZero() {
		t.Errorf("empty must yield zero time, got %v err=%v", t0, err)
	}
	if _, err := parseBackfillTime("garbage"); err == nil {
		t.Errorf("garbage must error")
	}
}

func TestChunkBounds(t *testing.T) {
	floor := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	chunk := 6 * time.Hour

	t.Run("reached_floor", func(t *testing.T) {
		_, _, reached := chunkBounds(floor, floor, chunk)
		if !reached {
			t.Error("oldestTs == floor must report reached=true")
		}
		_, _, reached = chunkBounds(floor.Add(-time.Hour), floor, chunk)
		if !reached {
			t.Error("oldestTs < floor must report reached=true")
		}
	})

	t.Run("normal_window", func(t *testing.T) {
		oldest := floor.Add(48 * time.Hour) // 2 days above floor
		from, to, reached := chunkBounds(oldest, floor, chunk)
		if reached {
			t.Fatal("normal window must not report reached=true")
		}
		if !to.Equal(oldest) {
			t.Errorf("to = %v, want %v", to, oldest)
		}
		if !from.Equal(oldest.Add(-chunk)) {
			t.Errorf("from = %v, want %v", from, oldest.Add(-chunk))
		}
	})

	t.Run("clamps_to_floor", func(t *testing.T) {
		// oldest just above floor + chunk would step below; should clamp.
		oldest := floor.Add(2 * time.Hour)
		from, to, reached := chunkBounds(oldest, floor, chunk)
		if reached {
			t.Fatal("oldest > floor must not report reached")
		}
		if !from.Equal(floor) {
			t.Errorf("from = %v, want %v (clamped)", from, floor)
		}
		if !to.Equal(oldest) {
			t.Errorf("to = %v, want %v", to, oldest)
		}
	})
}

func TestTruncateError(t *testing.T) {
	if got := truncateError(nil); got != "" {
		t.Errorf("nil → %q", got)
	}
	long := make([]byte, 1024)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateError(errors.New(string(long)))
	if len(got) > 512 {
		t.Errorf("truncate: got %d chars, want ≤ 512", len(got))
	}
	got2 := truncateError(errors.New("first line\nsecond line"))
	if got2 != "first line" {
		t.Errorf("multi-line truncate: got %q", got2)
	}
}
