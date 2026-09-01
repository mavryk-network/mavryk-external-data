package jobs

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/sony/gobreaker"
)

func newErrState() *repositories.BackfillState {
	return &repositories.BackfillState{Source: prices.SourceCoinGecko, EntityKey: "mvrk"}
}

// Crossing the threshold must PARK the entity for a cooldown — never set
// Disabled — and reset the counter for a fresh strike count.
func TestApplyBackfillError_ThresholdParksInsteadOfDisabling(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	st := newErrState()
	upstream := errors.New("HTTP 500 from upstream")

	var kind backfillErrorKind
	for i := 0; i < 5; i++ {
		kind, _ = applyBackfillError(st, 5, 2000, 16000, 60000, now, upstream)
	}

	if kind != backfillErrCooldown {
		t.Fatalf("5th failure with threshold 5 = kind %d, want cooldown", kind)
	}
	if st.Disabled {
		t.Fatal("threshold must cooldown, never permanently disable")
	}
	if st.DisabledReason != "" {
		t.Fatalf("disabled_reason = %q, want empty", st.DisabledReason)
	}
	if st.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0 (reset on cooldown)", st.ErrorCount)
	}
	if st.NextAttemptAt == nil || !st.NextAttemptAt.Equal(now.Add(backfillErrorCooldown)) {
		t.Fatalf("next_attempt_at = %v, want now+%v", st.NextAttemptAt, backfillErrorCooldown)
	}
	if st.LastError == "" {
		t.Fatal("last_error must be stamped (errorDrivenPause depends on it)")
	}
	if !errorDrivenPause(st) {
		t.Fatal("a cooldown park must read as error-driven")
	}
}

// Breaker-open reflects upstream health, not this entity: a brief pause,
// never an error-counter advance.
func TestApplyBackfillError_BreakerOpenDoesNotCount(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, berr := range []error{gobreaker.ErrOpenState, gobreaker.ErrTooManyRequests} {
		st := newErrState()
		st.ErrorCount = 3

		kind, wait := applyBackfillError(st, 5, 2000, 16000, 60000, now, berr)

		if kind != backfillErrBreakerPaused {
			t.Fatalf("%v: kind = %d, want breaker-paused", berr, kind)
		}
		if st.ErrorCount != 3 {
			t.Fatalf("%v: error_count = %d, want 3 (breaker must not count)", berr, st.ErrorCount)
		}
		if wait != breakerOpenRetryDelay {
			t.Fatalf("%v: wait = %v, want %v", berr, wait, breakerOpenRetryDelay)
		}
		if st.NextAttemptAt == nil || !st.NextAttemptAt.Equal(now.Add(breakerOpenRetryDelay)) {
			t.Fatalf("%v: next_attempt_at = %v", berr, st.NextAttemptAt)
		}
		if st.Disabled {
			t.Fatalf("%v: breaker pause must not disable", berr)
		}
	}
}

// The transport stack wraps errors on the way up.
func TestApplyBackfillError_WrappedBreakerError(t *testing.T) {
	now := time.Now().UTC()
	st := newErrState()
	wrapped := errors.Join(errors.New("fetch mvrk"), gobreaker.ErrOpenState)
	kind, _ := applyBackfillError(st, 5, 2000, 16000, 60000, now, wrapped)
	if kind != backfillErrBreakerPaused {
		t.Fatalf("wrapped ErrOpenState: kind = %d, want breaker-paused", kind)
	}
	if st.ErrorCount != 0 {
		t.Fatalf("error_count = %d, want 0", st.ErrorCount)
	}
}

// Below the threshold: exponential backoff, counter advances, never disabled.
func TestApplyBackfillError_TransientBacksOffExponentially(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	st := newErrState()
	upstream := errors.New("timeout")

	kind, wait1 := applyBackfillError(st, 5, 2000, 16000, 60000, now, upstream)
	if kind != backfillErrBackoff || st.ErrorCount != 1 {
		t.Fatalf("first failure: kind=%d count=%d, want backoff/1", kind, st.ErrorCount)
	}
	_, wait2 := applyBackfillError(st, 5, 2000, 16000, 60000, now, upstream)
	if st.ErrorCount != 2 {
		t.Fatalf("second failure: count=%d, want 2", st.ErrorCount)
	}
	if wait2 <= wait1 {
		t.Fatalf("backoff must grow: %v then %v", wait1, wait2)
	}
	if st.Disabled {
		t.Fatal("transient failures must never disable")
	}
}

// threshold <= 0 disables the cooldown mechanism entirely.
func TestApplyBackfillError_ZeroThresholdNeverCoolsDown(t *testing.T) {
	now := time.Now().UTC()
	st := newErrState()
	upstream := errors.New("boom")
	for i := 0; i < 25; i++ {
		if kind, _ := applyBackfillError(st, 0, 2000, 16000, 60000, now, upstream); kind != backfillErrBackoff {
			t.Fatalf("failure %d: kind = %d, want backoff", i, kind)
		}
	}
	if st.ErrorCount != 25 {
		t.Fatalf("error_count = %d, want 25", st.ErrorCount)
	}
}

// Sentinel pauses are steady-state — warning on them would spam one line per
// disabled/parked entity per tick, forever.
func TestShouldLogBackfillStepError(t *testing.T) {
	if shouldLogBackfillStepError(errBackfillSkipped) {
		t.Error("skipped entities must not be warned every tick")
	}
	if shouldLogBackfillStepError(errBackfillCoolingDown) {
		t.Error("cooldown entities must not be warned every tick")
	}
	if shouldLogBackfillStepError(fmt.Errorf("step: %w", errBackfillSkipped)) {
		t.Error("wrapped sentinels must be recognized too")
	}
	if !shouldLogBackfillStepError(errors.New("fetch failed")) {
		t.Error("real step errors must be logged")
	}
}

// A healthy caught-up park (clears LastError) must not read as an error
// cooldown, or the last-success gauge freezes on a healthy backfill.
func TestErrorDrivenPause_Discriminates(t *testing.T) {
	st := newErrState()
	pauseUntilNextFill(st, time.Now().UTC())
	if errorDrivenPause(st) {
		t.Fatal("caught-up park must not read as error-driven")
	}
	_, _ = applyBackfillError(st, 5, 2000, 16000, 60000, time.Now().UTC(), errors.New("x"))
	if !errorDrivenPause(st) {
		t.Fatal("an error backoff must read as error-driven")
	}
}
