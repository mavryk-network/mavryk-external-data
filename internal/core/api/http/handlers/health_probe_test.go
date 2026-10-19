package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// N checks within one TTL must cost exactly one underlying ping — the cache
// is what bounds DB work under a /readyz flood.
func TestCachedProbe_CoalescesWithinTTL(t *testing.T) {
	var calls int
	p := &cachedProbe{ttl: time.Hour, probe: func(context.Context) error {
		calls++
		return nil
	}}
	for i := 0; i < 50; i++ {
		if err := p.check(); err != nil {
			t.Fatalf("check %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("50 checks within TTL cost %d pings, want 1", calls)
	}
}

func TestCachedProbe_ErrorVerdictIsCachedAndExpires(t *testing.T) {
	var calls int
	fail := errors.New("db down")
	p := &cachedProbe{ttl: 20 * time.Millisecond, probe: func(context.Context) error {
		calls++
		if calls == 1 {
			return fail
		}
		return nil
	}}
	if err := p.check(); !errors.Is(err, fail) {
		t.Fatalf("first check = %v, want the probe error", err)
	}
	if err := p.check(); !errors.Is(err, fail) {
		t.Fatal("verdict must be cached within TTL (error included)")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	time.Sleep(25 * time.Millisecond)
	if err := p.check(); err != nil {
		t.Fatalf("after TTL the probe must re-run: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// Concurrent checks must coalesce onto ONE in-flight ping.
func TestCachedProbe_ConcurrentChecksShareOnePing(t *testing.T) {
	var mu sync.Mutex
	var calls int
	p := &cachedProbe{ttl: time.Hour, probe: func(context.Context) error {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		return nil
	}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.check(); err != nil {
				t.Errorf("check: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls != 1 {
		t.Fatalf("20 concurrent checks cost %d pings, want 1", calls)
	}
}
