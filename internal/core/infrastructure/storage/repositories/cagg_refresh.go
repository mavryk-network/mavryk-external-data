package repositories

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Continuous-aggregate views backing chart / ATH reads. A closed set from the
// migrations — never user input, so interpolating them into SQL is safe.
var (
	tokenCandleAggregates = []string{"token_prices_1m", "token_prices_1h", "token_prices_1d"}
	rwaCandleAggregates   = []string{"rwa_quote_prices_1m", "rwa_quote_prices_1h", "rwa_quote_prices_1d"}
)

// RefreshCandleAggregates materializes [from, to] into the token_prices_*
// aggregates. Called per backfill chunk: that history sits below the refresh
// policy's start_offset, so nothing else ever materializes it, and chart reads
// never scan raw token_prices.
func (r *TokenPriceRepository) RefreshCandleAggregates(ctx context.Context, from, to time.Time) error {
	return refreshContinuousAggregates(ctx, r.db, tokenCandleAggregates, from, to)
}

// RefreshRWACandleAggregates is the rwa_quote_prices_* counterpart, called by
// the Equiteez backfill after a batch of fills lands.
func (r *LookupRepository) RefreshRWACandleAggregates(ctx context.Context, from, to time.Time) error {
	return refreshContinuousAggregates(ctx, r.db, rwaCandleAggregates, from, to)
}

// refreshContinuousAggregates CALLs refresh_continuous_aggregate per view.
// No-ops on a degenerate window or without TimescaleDB. Each view uses a bare
// Exec: the procedure cannot run inside a transaction block. Callers treat the
// error as best-effort — a refresh failure must never fail the backfill step.
func refreshContinuousAggregates(ctx context.Context, db *gorm.DB, views []string, from, to time.Time) error {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil
	}
	ready, err := timescaleReady(ctx, db)
	if err != nil {
		// A failed probe is not "extension absent" — surface it, or the skipped
		// window is never revisited (the caller's cursor already advanced).
		return fmt.Errorf("timescaledb probe: %w", err)
	}
	if !ready {
		return nil
	}
	var firstErr error
	for _, v := range views {
		// TimescaleDB refreshes only buckets FULLY enclosed by the window
		// ("refresh window too small" otherwise) — widen to bucket boundaries
		// so a 6h chunk can still materialize the 1d views.
		width := viewBucketWidth(v)
		wf, wt := alignToBucket(from.UTC(), to.UTC(), width)
		if width > 0 {
			// Never materialize the IN-PROGRESS bucket: that advances the
			// watermark past it, and the real-time union (0021) only covers
			// raw data past the watermark — the live edge would freeze at its
			// refresh-time snapshot (up to a day on the 1d views).
			if lastComplete := time.Now().UTC().Truncate(width); wt.After(lastComplete) {
				wt = lastComplete
			}
			if !wf.Before(wt) {
				continue // whole window is in-progress — the real-time union serves it
			}
		}
		// v is from a closed constant set (see above) — safe to interpolate.
		// The ::timestamptz casts are required: the procedure's window args
		// are polymorphic, and an uncast bound parameter fails inference with
		// SQLSTATE 42P18.
		stmt := fmt.Sprintf("CALL refresh_continuous_aggregate('%s', ?::timestamptz, ?::timestamptz)", v)
		if err := db.WithContext(ctx).Exec(stmt, wf, wt).Error; err != nil && firstErr == nil {
			firstErr = fmt.Errorf("refresh %s: %w", v, err)
		}
	}
	return firstErr
}

// viewBucketWidth maps a CA view name to its time_bucket width. The suffix
// convention is fixed by the migrations that create these views.
func viewBucketWidth(view string) time.Duration {
	switch {
	case strings.HasSuffix(view, "_1m"):
		return time.Minute
	case strings.HasSuffix(view, "_1h"):
		return time.Hour
	case strings.HasSuffix(view, "_1d"):
		return 24 * time.Hour
	default:
		return 0
	}
}

// alignToBucket widens [from, to] outward to bucket boundaries so the window
// fully encloses every bucket it touches. time.Truncate on UTC snaps to the
// same epoch-aligned origin time_bucket uses for 1m/1h/1d, and widening is
// safe — refresh re-materializes touched buckets from raw data.
func alignToBucket(from, to time.Time, width time.Duration) (time.Time, time.Time) {
	if width <= 0 {
		return from, to
	}
	wf := from.Truncate(width)
	wt := to.Truncate(width)
	if wt.Before(to) {
		wt = wt.Add(width)
	}
	// Degenerate equal-bound windows still get one full bucket.
	if !wf.Before(wt) {
		wt = wf.Add(width)
	}
	return wf, wt
}

var (
	timescaleMu    sync.Mutex
	timescaleState int // 0 = unknown (probe again), 1 = present, -1 = absent
)

// timescaleReady reports whether the timescaledb extension is installed.
// Only a successful probe is cached (presence does not change at runtime); a
// probe failure returns the error and stays unknown, so the next call
// re-probes instead of latching a blip as "absent" for the process lifetime.
func timescaleReady(ctx context.Context, db *gorm.DB) (bool, error) {
	timescaleMu.Lock()
	defer timescaleMu.Unlock()
	if timescaleState != 0 {
		return timescaleState == 1, nil
	}
	if db == nil {
		return false, nil
	}
	var n int64
	err := db.WithContext(ctx).
		Raw("SELECT count(*) FROM pg_extension WHERE extname = 'timescaledb'").
		Scan(&n).Error
	if err != nil {
		return false, err
	}
	if n > 0 {
		timescaleState = 1
	} else {
		timescaleState = -1
	}
	return timescaleState == 1, nil
}
