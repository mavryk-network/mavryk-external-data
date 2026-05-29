package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cloneInts(in []int) []int {
	if in == nil {
		return nil
	}
	out := make([]int, len(in))
	copy(out, in)
	return out
}

func TestTTL_HitMiss(t *testing.T) {
	c := New[[]int](time.Minute, cloneInts)
	if _, ok := c.Lookup("k"); ok {
		t.Fatal("empty cache lookup must miss")
	}
	c.Store("k", []int{1, 2, 3})
	v, ok := c.Lookup("k")
	if !ok || len(v) != 3 || v[2] != 3 {
		t.Fatalf("hit got %v ok=%v", v, ok)
	}
}

func TestTTL_Expiry(t *testing.T) {
	c := New[int](20*time.Millisecond, nil)
	c.Store("k", 42)
	if _, ok := c.Lookup("k"); !ok {
		t.Fatal("fresh entry must hit")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Lookup("k"); ok {
		t.Fatal("expired entry must miss")
	}
}

func TestTTL_DisabledNoCache(t *testing.T) {
	c := New[int](0, nil)
	if c.Enabled() {
		t.Fatal("zero ttl must not be Enabled()")
	}
	c.Store("k", 1)
	if _, ok := c.Lookup("k"); ok {
		t.Fatal("disabled cache must miss after Store")
	}
}

func TestTTL_GetOrLoadCachesSuccess(t *testing.T) {
	c := New[int](time.Minute, nil)
	var calls atomic.Int32
	load := func(ctx context.Context) (int, error) {
		calls.Add(1)
		return 7, nil
	}
	for i := 0; i < 5; i++ {
		v, err := c.GetOrLoad(context.Background(), "k", load)
		if err != nil || v != 7 {
			t.Fatalf("got %d err=%v", v, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
}

func TestTTL_GetOrLoadDoesNotCacheError(t *testing.T) {
	c := New[int](time.Minute, nil)
	boom := errors.New("boom")
	calls := 0
	load := func(ctx context.Context) (int, error) {
		calls++
		return 0, boom
	}
	for i := 0; i < 3; i++ {
		if _, err := c.GetOrLoad(context.Background(), "k", load); !errors.Is(err, boom) {
			t.Fatalf("err = %v want %v", err, boom)
		}
	}
	if calls != 3 {
		t.Fatalf("loader calls = %d, want 3 (errors must not cache)", calls)
	}
}

func TestTTL_InvalidatePredicate(t *testing.T) {
	c := New[int](time.Minute, nil)
	c.Store("mvrk:usd", 1)
	c.Store("mvrk:eur", 2)
	c.Store("usdt:usd", 3)
	c.Invalidate(func(k string) bool {
		return k == "mvrk:usd" || k == "mvrk:eur"
	})
	for _, k := range []string{"mvrk:usd", "mvrk:eur"} {
		if _, ok := c.Lookup(k); ok {
			t.Fatalf("%q should be evicted", k)
		}
	}
	if v, ok := c.Lookup("usdt:usd"); !ok || v != 3 {
		t.Fatalf("usdt:usd missing after partial invalidate")
	}
}

func TestTTL_Purge(t *testing.T) {
	c := New[int](time.Minute, nil)
	c.Store("a", 1)
	c.Store("b", 2)
	c.Purge()
	if _, ok := c.Lookup("a"); ok {
		t.Fatal("Purge must drop entry a")
	}
	if _, ok := c.Lookup("b"); ok {
		t.Fatal("Purge must drop entry b")
	}
}

// TestTTL_CloneIsolatesCallers: callers mutating returned slices must not
// poison the cache.
func TestTTL_CloneIsolatesCallers(t *testing.T) {
	c := New[[]int](time.Minute, cloneInts)
	c.Store("k", []int{1, 2, 3})
	got, _ := c.Lookup("k")
	got[0] = 99
	again, _ := c.Lookup("k")
	if again[0] != 1 {
		t.Fatalf("clone failed: cache poisoned, got %v", again)
	}
}

// TestTTL_Race: run under -race; if locking is wrong the race detector trips.
func TestTTL_Race(t *testing.T) {
	c := New[int](time.Minute, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = c.GetOrLoad(context.Background(), "k", func(context.Context) (int, error) { return i, nil })
				_, _ = c.Lookup("k")
			}
		}(i)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Invalidate(func(string) bool { return true })
			}
		}()
	}
	wg.Wait()
}
