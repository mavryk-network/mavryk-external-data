package repositories

import (
	"context"
	"fmt"
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
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil
	}
	// Timescale refreshes only complete buckets. Include the enclosing UTC days
	// so even a one-trade batch refreshes the 1m, 1h and 1d chart buckets.
	start := from.UTC().Truncate(24 * time.Hour)
	end := to.UTC().Truncate(24 * time.Hour)
	if end.Before(to) {
		end = end.Add(24 * time.Hour)
	}
	return refreshContinuousAggregates(ctx, r.db, rwaCandleAggregates, start, end)
}

// refreshContinuousAggregates runs CALL refresh_continuous_aggregate(view, from, to)
// for each view. It is deliberately defensive:
//
//   - No-op on a degenerate window (zero bounds or from >= to).
//   - No-op on a plain-Postgres deployment (timescaledb absent). Failed probes
//     return an error so a temporary outage cannot silently skip materialization.
//   - refresh_continuous_aggregate cannot run inside a transaction block, so each
//     view is refreshed with a bare Exec on the pool (GORM does not open a
//     transaction for a single Exec).
//
// Equiteez retries a failed refresh before advancing its history cursor. Other
// callers can still choose best-effort refresh behavior.
func refreshContinuousAggregates(ctx context.Context, db *gorm.DB, views []string, from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil
	}
	available, err := timescaleAvailable(ctx, db)
	if err != nil {
		return err
	}
	if !available {
		return nil
	}
	var firstErr error
	for _, v := range views {
		// v is from a closed constant set (see above) — safe to interpolate.
		// The window bounds are bound parameters.
		stmt := fmt.Sprintf("CALL refresh_continuous_aggregate('%s', ?::timestamptz, ?::timestamptz)", v)
		if err := db.WithContext(ctx).Exec(stmt, from.UTC(), to.UTC()).Error; err != nil && firstErr == nil {
			firstErr = fmt.Errorf("refresh %s: %w", v, err)
		}
	}
	return firstErr
}

// Probe the current database on each batch. A failed or canceled probe must not
// be cached as extension absence, and separate pools may target different DBs.
func timescaleAvailable(ctx context.Context, db *gorm.DB) (bool, error) {
	var n int64
	if err := db.WithContext(ctx).
		Raw("SELECT count(*) FROM pg_extension WHERE extname = 'timescaledb'").
		Scan(&n).Error; err != nil {
		return false, fmt.Errorf("probe TimescaleDB extension: %w", err)
	}
	return n > 0, nil
}
