package prices

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

type fakeRepo struct {
	mu        sync.Mutex
	queries   int
	saveCalls int
	last      []prices.PricePoint
	saveErr   error
	queryErr  error
}

func (f *fakeRepo) Save(_ context.Context, points []prices.PricePoint) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return 0, f.saveErr
	}
	f.saveCalls++
	f.last = make([]prices.PricePoint, len(points))
	copy(f.last, points)
	return int64(len(points)), nil
}

func (f *fakeRepo) Query(_ context.Context, _ prices.Query) ([]prices.PricePoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	f.queries++
	return cloneSlice(f.last), nil
}

func samplePoint(t time.Time, metric string, price float64) prices.PricePoint {
	return prices.PricePoint{
		Source:    prices.SourceCoinGecko,
		EntityKey: "mvrk",
		Timestamp: t,
		Metric:    metric,
		Price:     decimal.NewFromFloat(price),
	}
}

func TestCachedRepository_LatestQueryHitsCache(t *testing.T) {
	now := time.Now().UTC()
	inner := &fakeRepo{last: []prices.PricePoint{samplePoint(now, "usd", 1)}}
	c := NewCachedRepository(inner, time.Minute)

	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"} // IsLatest
	for i := 0; i < 3; i++ {
		if _, err := c.Query(context.Background(), q); err != nil {
			t.Fatalf("query #%d: %v", i, err)
		}
	}
	if inner.queries != 1 {
		t.Errorf("inner.queries = %d, want 1 (cache should serve subsequent calls)", inner.queries)
	}
}

func TestCachedRepository_TimeWindowSkipsCache(t *testing.T) {
	inner := &fakeRepo{}
	c := NewCachedRepository(inner, time.Minute)

	q := prices.Query{
		Source:    prices.SourceCoinGecko,
		EntityKey: "mvrk",
		From:      time.Now().Add(-time.Hour),
		To:        time.Now(),
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Query(context.Background(), q); err != nil {
			t.Fatalf("query #%d: %v", i, err)
		}
	}
	if inner.queries != 3 {
		t.Errorf("inner.queries = %d, want 3 (window queries must not cache)", inner.queries)
	}
}

func TestCachedRepository_TTLExpiry(t *testing.T) {
	inner := &fakeRepo{last: []prices.PricePoint{samplePoint(time.Now(), "usd", 1)}}
	c := NewCachedRepository(inner, 50*time.Millisecond)
	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}

	if _, err := c.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := c.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if inner.queries != 2 {
		t.Errorf("inner.queries = %d, want 2 (TTL must expire)", inner.queries)
	}
}

func TestCachedRepository_SaveInvalidates(t *testing.T) {
	now := time.Now()
	inner := &fakeRepo{last: []prices.PricePoint{samplePoint(now, "usd", 1)}}
	c := NewCachedRepository(inner, time.Minute)
	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}

	if _, err := c.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if inner.queries != 1 {
		t.Fatalf("expected 1 inner query, got %d", inner.queries)
	}
	// Save with a matching (source, entity) — invalidates the cached entry.
	_, err := c.Save(context.Background(), []prices.PricePoint{samplePoint(now, "usd", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Query(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if inner.queries != 2 {
		t.Errorf("inner.queries = %d, want 2 after Save invalidation", inner.queries)
	}
}

func TestCachedRepository_DisabledTTL(t *testing.T) {
	inner := &fakeRepo{}
	c := NewCachedRepository(inner, 0)
	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}
	for i := 0; i < 3; i++ {
		if _, err := c.Query(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if inner.queries != 3 {
		t.Errorf("inner.queries = %d, want 3 (ttl=0 disables cache)", inner.queries)
	}
}

func TestCachedRepository_PassesErrorThrough(t *testing.T) {
	wantErr := errors.New("boom")
	inner := &fakeRepo{queryErr: wantErr}
	c := NewCachedRepository(inner, time.Minute)
	if _, err := c.Query(context.Background(), prices.Query{}); !errors.Is(err, wantErr) {
		t.Errorf("Query: got %v, want %v", err, wantErr)
	}
}

// TestCachedRepository_ConcurrentReadsWrites is run under -race; if the cache's
// locking is wrong the race detector will scream. We just need many goroutines
// hammering the cache simultaneously.
func TestCachedRepository_ConcurrentReadsWrites(t *testing.T) {
	now := time.Now()
	inner := &fakeRepo{last: []prices.PricePoint{samplePoint(now, "usd", 1)}}
	c := NewCachedRepository(inner, time.Minute)

	q := prices.Query{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = c.Query(ctx, q)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = c.Save(ctx, []prices.PricePoint{samplePoint(now, "usd", float64(seed*100+j))})
			}
		}(i)
	}
	wg.Wait()
}
