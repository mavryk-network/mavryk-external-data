package cache

import (
	"context"
	"testing"
	"time"
)

// TestGetOrLoad_SkipsStoreAfterInvalidate pins the load/store vs invalidate
// race: a value loaded BEFORE an invalidation must not be cached AFTER it —
// that would serve pre-write data for a full TTL.
func TestGetOrLoad_SkipsStoreAfterInvalidate(t *testing.T) {
	c := New[string](time.Minute, nil)

	invalidateRan := false
	got, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, error) {
		// Simulate a concurrent writer committing + invalidating mid-load.
		c.Invalidate(func(string) bool { return true })
		invalidateRan = true
		return "stale", nil
	})
	if err != nil || got != "stale" {
		t.Fatalf("GetOrLoad = %q, %v", got, err)
	}
	if !invalidateRan {
		t.Fatal("test setup broken")
	}
	if v, ok := c.Lookup("k"); ok {
		t.Fatalf("stale value %q was cached across an invalidation", v)
	}

	// A load with no concurrent invalidation still caches normally.
	_, _ = c.GetOrLoad(context.Background(), "k2", func(context.Context) (string, error) {
		return "fresh", nil
	})
	if v, ok := c.Lookup("k2"); !ok || v != "fresh" {
		t.Fatalf("normal store broken: %q, %v", v, ok)
	}
}
