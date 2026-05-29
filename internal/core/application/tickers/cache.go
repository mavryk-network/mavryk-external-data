package tickers

import (
	"context"
	"strconv"
	"time"

	"quotes/internal/core/application/cache"
	"quotes/internal/core/domain/tickers"
)

// CachedRepository wraps a Repository with per-endpoint TTL caches.
//
// Two independent caches because the two endpoints have different cost and
// call-rate profiles:
//   - /latest        — single DISTINCT ON + LATERAL, called every render
//   - /distribution  — GROUP BY across all latest rows, called less often
//
// Both keys include IncludeStale / GroupBy / StaleAfter so a flip of any
// dimension doesn't return a value from a sibling. SaveSnapshot invalidates
// EVERYTHING for the touched token — the job rewrites the whole snapshot, so
// keeping partial entries around is wasted memory.
type CachedRepository struct {
	inner        Repository
	latest       *cache.TTL[tickers.Snapshot]
	distribution *cache.TTL[tickers.Distribution]
}

// NewCachedRepository builds the decorator. Either ttl may be 0 to disable
// that endpoint's cache; the wrapper still satisfies Repository.
func NewCachedRepository(inner Repository, latestTTL, distributionTTL time.Duration) *CachedRepository {
	return &CachedRepository{
		inner:        inner,
		latest:       cache.New(latestTTL, cloneSnapshot),
		distribution: cache.New(distributionTTL, cloneDistribution),
	}
}

// SaveSnapshot writes through and purges the caches for the affected token.
// In v1 the job is single-token (MVRK) so a full Purge is the same as
// per-token invalidate; we still scope by token to stay future-proof.
func (c *CachedRepository) SaveSnapshot(
	ctx context.Context,
	exchanges []tickers.Exchange,
	rows []tickers.Ticker,
) (int64, error) {
	n, err := c.inner.SaveSnapshot(ctx, exchanges, rows)
	if err != nil {
		return n, err
	}
	if n > 0 {
		// Build the set of tokens we touched and drop matching cache keys.
		affected := make(map[string]struct{}, 2)
		for _, r := range rows {
			affected[string(r.Token)] = struct{}{}
		}
		evict := func(key string) bool {
			// Key encoding (see latestKey/distributionKey): "<token>|...".
			// Compare by token prefix.
			for tok := range affected {
				if len(key) >= len(tok)+1 && key[:len(tok)] == tok && key[len(tok)] == '|' {
					return true
				}
			}
			return false
		}
		c.latest.Invalidate(evict)
		c.distribution.Invalidate(evict)
	}
	return n, nil
}

func (c *CachedRepository) LatestSnapshot(ctx context.Context, q LatestQuery) (tickers.Snapshot, error) {
	if !c.latest.Enabled() {
		return c.inner.LatestSnapshot(ctx, q)
	}
	key := latestKey(q)
	return c.latest.GetOrLoad(ctx, key, func(ctx context.Context) (tickers.Snapshot, error) {
		return c.inner.LatestSnapshot(ctx, q)
	})
}

func (c *CachedRepository) VolumeDistribution(ctx context.Context, q DistributionQuery) (tickers.Distribution, error) {
	if !c.distribution.Enabled() {
		return c.inner.VolumeDistribution(ctx, q)
	}
	key := distributionKey(q)
	return c.distribution.GetOrLoad(ctx, key, func(ctx context.Context) (tickers.Distribution, error) {
		return c.inner.VolumeDistribution(ctx, q)
	})
}

func latestKey(q LatestQuery) string {
	// token | source | includeStale | staleAfterMS
	stale := "0"
	if q.IncludeStale {
		stale = "1"
	}
	return string(q.Token) + "|" + string(q.Source) + "|" + stale + "|" +
		strconv.FormatInt(q.StaleAfter.Milliseconds(), 10)
}

func distributionKey(q DistributionQuery) string {
	return string(q.Token) + "|" + string(q.Source) + "|" + string(q.GroupBy) + "|" +
		strconv.FormatInt(q.StaleAfter.Milliseconds(), 10)
}

func cloneSnapshot(s tickers.Snapshot) tickers.Snapshot {
	out := s
	if s.Rows != nil {
		out.Rows = make([]tickers.SnapshotRow, len(s.Rows))
		copy(out.Rows, s.Rows)
	}
	return out
}

func cloneDistribution(d tickers.Distribution) tickers.Distribution {
	out := d
	if d.Rows != nil {
		out.Rows = make([]tickers.DistributionRow, len(d.Rows))
		copy(out.Rows, d.Rows)
	}
	return out
}
