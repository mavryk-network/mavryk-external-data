// Package jobs hosts the background collectors (CoinGecko live + backfill, Equiteez
// RWA orderbook). Each job exposes Start(ctx)/Stop() and writes through the
// application Repository (so the cache decorator invalidates as a side-effect).
package jobs

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// runTickerLoop runs `tick` immediately, then every interval, until ctx is
// cancelled or stopCh closes. jitter (> 0) delays the FIRST tick by a random
// duration in [0, jitter) so replicas don't hammer upstream in lockstep.
//
// A tick returns an error when it could not do its work; only a successful one
// advances metrics.JobLastSuccessTimestamp, so its staleness alert can see a
// dead upstream.
func runTickerLoop(
	ctx context.Context,
	stopCh <-chan struct{},
	interval, jitter time.Duration,
	logger *zerolog.Logger,
	name string,
	tick func(ctx context.Context) error,
) {
	// Seed BEFORE the jitter sleep: a GaugeVec with no children is absent from
	// /metrics entirely, so a from-boot outage could not alert.
	metrics.JobLastSuccessTimestamp.WithLabelValues(name).SetToCurrentTime()

	if jitter > 0 {
		// [0, jitter): a ±jitter offset around a common start point collapsed to
		// zero delay ~half the time, defeating the anti-thundering-herd purpose.
		offset := time.Duration(rand.Int63n(int64(jitter))) //nolint:gosec // not security-sensitive
		select {
		case <-time.After(offset):
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		}
	}

	budget := tickBudget(interval)
	runTickWithCorrelation(ctx, budget, logger, name, tick)

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if logger != nil {
				logger.Info().Msg("ticker_loop_stopped_context")
			}
			return
		case <-stopCh:
			if logger != nil {
				logger.Info().Msg("ticker_loop_stopped")
			}
			return
		case <-t.C:
			runTickWithCorrelation(ctx, budget, logger, name, tick)
		}
	}
}

// tickBudget bounds one tick — without it a TCP black hole stalls the job
// forever and Stop() blocks until SIGKILL. max(2×interval, 5m) keeps
// slow-but-progressing work from being cut.
func tickBudget(interval time.Duration) time.Duration {
	const floor = 5 * time.Minute
	if b := 2 * interval; b > floor {
		return b
	}
	return floor
}

// defaultJitter staggers the first tick across replicas.
func defaultJitter(interval time.Duration) time.Duration {
	return interval / 10
}

// runTickWithCorrelation injects a fresh tick_id into ctx (re-using the
// request_id key, so the logger and HTTPTransport pick it up) and recovers
// panics PER TICK — a panic unwinding past this frame would kill the ticker
// goroutine and silently stop the job until restart.
func runTickWithCorrelation(ctx context.Context, timeout time.Duration, logger *zerolog.Logger, name string, tick func(context.Context) error) {
	tickID := "tick-" + uuid.NewString()
	tickCtx := logging.WithRequestID(ctx, tickID)
	if timeout > 0 {
		var cancel context.CancelFunc
		tickCtx, cancel = context.WithTimeout(tickCtx, timeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			metrics.JobTickPanicsTotal.WithLabelValues(name).Inc()
			if logger != nil {
				logger.Error().Interface("panic", r).Str("job", name).Str("tick_id", tickID).Msg("job_tick_panic_recovered")
			}
		}
	}()
	if err := tick(tickCtx); err != nil {
		if logger != nil {
			// A cancelled tick is an ordinary shutdown drain, not an incident —
			// keep it out of the warning stream, but still withhold the stamp.
			level := logger.Warn()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				level = logger.Debug()
			}
			level.Err(err).Str("job", name).Str("tick_id", tickID).Msg("job_tick_unsuccessful")
		}
		return
	}
	metrics.JobLastSuccessTimestamp.WithLabelValues(name).SetToCurrentTime()
}

// tickOutcome tallies per-entity results so a tick walking many entities can
// report one verdict. Skips abstain rather than counting as successes: they are
// permanent states here (a token at its floor skips forever), and one finished
// token must not keep the last-success gauge fresh while the rest fail.
type tickOutcome struct {
	worked  int
	failed  int
	skipped int
}

func (o *tickOutcome) record(err error) {
	switch {
	case err == nil:
		o.worked++
	case errors.Is(err, errBackfillSkipped):
		o.skipped++
	default:
		o.failed++
	}
}

// verdict fails only when NOTHING got through. Partial success is progress, or
// one broken entity would mask every healthy one behind the same job.
func (o tickOutcome) verdict(ctx context.Context, what string) error {
	if o.worked > 0 {
		return nil
	}
	if o.failed > 0 {
		return fmt.Errorf("%s: all %d acting entities failed", what, o.failed)
	}
	if o.skipped > 0 {
		return nil // nothing to do this tick
	}
	// Nothing attempted: no work at all (nil), or cancelled before starting.
	return ctx.Err()
}

// safeGo runs f in a new goroutine with panic-recovery, logging the panic via
// the supplied logger. wg.Done() is called whether f returns normally or panics.
func safeGo(wg *sync.WaitGroup, logger *zerolog.Logger, name string, f func()) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil && logger != nil {
				logger.Error().Interface("panic", r).Str("goroutine", name).Msg("background_panic")
			}
		}()
		f()
	}()
}
