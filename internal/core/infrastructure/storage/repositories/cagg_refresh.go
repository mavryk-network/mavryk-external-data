package repositories

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Continuous-aggregate views backing chart / ATH reads. Names come from a closed
// set defined in migrations (0006/0007/0010/0011); they are never user input, so
// interpolating them into the CALL statement below is safe.
var (
	tokenCandleAggregates = []string{"token_prices_1m", "token_prices_1h", "token_prices_1d"}
	rwaCandleAggregates   = []string{"rwa_quote_prices_1m", "rwa_quote_prices_1h", "rwa_quote_prices_1d"}
)

// RefreshCandleAggregates materializes the [from, to] window into the
// token_prices_* continuous aggregates. The backfill job calls it after each
// chunk lands so backfilled history — which sits below the CA refresh-policy
// start_offset and is therefore never materialized by the scheduler — becomes
// visible to chart reads (QueryCandles never scans raw token_prices).
func (r *TokenPriceRepository) RefreshCandleAggregates(ctx context.Context, from, to time.Time) error {
	return refreshContinuousAggregates(ctx, r.db, tokenCandleAggregates, from, to)
}

// RefreshRWACandleAggregates materializes [from, to] into the rwa_quote_prices_*
// continuous aggregates. Called by the Equiteez backfill job after a batch of
// fills lands, for the same reason as the FT side.
func (r *LookupRepository) RefreshRWACandleAggregates(ctx context.Context, from, to time.Time) error {
	return refreshContinuousAggregates(ctx, r.db, rwaCandleAggregates, from, to)
}

// refreshContinuousAggregates runs CALL refresh_continuous_aggregate(view, from, to)
// for each view. It is deliberately defensive:
//
//   - No-op on a degenerate window (zero bounds or from >= to).
//   - No-op on a plain-Postgres deployment (timescaledb absent) — detected once
//     and cached, so the price path never pays a probe per call.
//   - refresh_continuous_aggregate cannot run inside a transaction block, so each
//     view is refreshed with a bare Exec on the pool (GORM does not open a
//     transaction for a single Exec).
//
// Callers treat the returned error as best-effort: a refresh failure must never
// fail or retry the backfill step (worst case the CA simply stays as stale as it
// is today).
func refreshContinuousAggregates(ctx context.Context, db *gorm.DB, views []string, from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil
	}
	if !timescaleAvailable(ctx, db) {
		return nil
	}
	var firstErr error
	for _, v := range views {
		// v is from a closed constant set (see above) — safe to interpolate.
		// The window bounds are bound parameters.
		stmt := fmt.Sprintf("CALL refresh_continuous_aggregate('%s', ?, ?)", v)
		if err := db.WithContext(ctx).Exec(stmt, from.UTC(), to.UTC()).Error; err != nil && firstErr == nil {
			firstErr = fmt.Errorf("refresh %s: %w", v, err)
		}
	}
	return firstErr
}

var (
	timescaleOnce    sync.Once
	timescalePresent bool
)

// timescaleAvailable reports whether the timescaledb extension is installed,
// caching the answer for the process lifetime (extension presence does not change
// at runtime). A probe failure is treated as "absent" so refresh degrades to a
// no-op rather than erroring on every call.
func timescaleAvailable(ctx context.Context, db *gorm.DB) bool {
	timescaleOnce.Do(func() {
		if db == nil {
			return
		}
		var n int64
		err := db.WithContext(ctx).
			Raw("SELECT count(*) FROM pg_extension WHERE extname = 'timescaledb'").
			Scan(&n).Error
		timescalePresent = err == nil && n > 0
	})
	return timescalePresent
}
