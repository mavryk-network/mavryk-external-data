package jobs

import (
	"errors"
	"time"

	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/sony/gobreaker"
)

// backfillErrorKind classifies what a failed backfill step did to the
// persisted state — the caller picks metrics/log lines from it.
type backfillErrorKind int

const (
	// Shared circuit breaker is open — upstream health, not this entity's
	// fault: the error counter is NOT advanced.
	backfillErrBreakerPaused backfillErrorKind = iota
	// Consecutive-error threshold crossed — parked for backfillErrorCooldown,
	// counter reset. Never a permanent disable.
	backfillErrCooldown
	// Transient failure below the threshold — exponential backoff.
	backfillErrBackoff
)

// shouldLogBackfillStepError filters the per-entity tick warn: both sentinel
// pauses are steady-state (and already Debug-logged inside the step), so
// warning on them would spam every tick.
func shouldLogBackfillStepError(err error) bool {
	return !errors.Is(err, errBackfillCoolingDown) && !errors.Is(err, errBackfillSkipped)
}

// applyBackfillError is the single classification shared by both backfill
// jobs' recordError. It mutates only `st` (persistence, metrics and logging
// stay with the caller), so the semantics are unit-testable without a
// database. Every path leaves LastError non-empty — errorDrivenPause keys on
// that to tell an error pause from a healthy caught-up park.
func applyBackfillError(
	st *repositories.BackfillState,
	threshold, backoffInitialMs, backoffMaxMs, maxBackoffMs int,
	now time.Time,
	fetchErr error,
) (backfillErrorKind, time.Duration) {
	st.LastError = truncateError(fetchErr)

	if errors.Is(fetchErr, gobreaker.ErrOpenState) || errors.Is(fetchErr, gobreaker.ErrTooManyRequests) {
		next := now.Add(breakerOpenRetryDelay)
		st.NextAttemptAt = &next
		return backfillErrBreakerPaused, breakerOpenRetryDelay
	}

	st.ErrorCount++
	if threshold > 0 && st.ErrorCount >= threshold {
		next := now.Add(backfillErrorCooldown)
		st.NextAttemptAt = &next
		st.ErrorCount = 0
		return backfillErrCooldown, backfillErrorCooldown
	}

	backoff := computeBackoff(st.ErrorCount, backoffInitialMs, backoffMaxMs, maxBackoffMs)
	next := now.Add(backoff)
	st.NextAttemptAt = &next
	return backfillErrBackoff, backoff
}
