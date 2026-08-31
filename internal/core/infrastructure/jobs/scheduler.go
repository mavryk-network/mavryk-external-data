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

// runTickerLoop runs `tick` once immediately, then on every interval, until the
// context is cancelled or the stop channel closes. Centralizes the boilerplate
// each job otherwise duplicates (immediate first call, ticker, ctx-aware exit,
// per-tick correlation id).
//
// jitter (when > 0) delays the FIRST tick by a random duration in [0, jitter) —
// good practice for multi-replica deployments to avoid synchronized hammering of
// upstream on a coordinated restart.
//
// Each tick gets a fresh `tick_id` (uuid) propagated via context so all log
// lines emitted while the tick is running can be correlated post-hoc.
// A tick returns an error when it could not do its work — a failed fetch or
// save, or (for ticks that walk many entities) every entity failing. That
// verdict, not merely "returned without panicking", is what advances
// metrics.JobLastSuccessTimestamp, so the staleness alert on that gauge can
// actually see a dead upstream.
func runTickerLoop(
	ctx context.Context,
	stopCh <-chan struct{},
	interval, jitter time.Duration,
	logger *zerolog.Logger,
	name string,
	tick func(ctx context.Context) error,
) {
	// Seed the gauge before the jitter sleep, not after: a GaugeVec with no
	// children is absent from /metrics entirely, so until this runs the job
	// exports nothing at all and a from-boot outage cannot alert. The hourly
	// pair-sync jitters up to six minutes, and a fast crash-loop would never
	// reach the tick.
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

// tickBudget bounds one tick: without a deadline a TCP black hole or a
// lock-blocked query stalls the (single-goroutine) job silently forever, and
// Stop() blocks shutdown until SIGKILL. Generous — max(2×interval, 5m) — so
// slow-but-progressing work (backfill CAGG refreshes) never gets cut.
func tickBudget(interval time.Duration) time.Duration {
	const floor = 5 * time.Minute
	if b := 2 * interval; b > floor {
		return b
	}
	return floor
}

// defaultJitter staggers the first tick across replicas: a tenth of the
// interval breaks fleet-synchronized upstream hammering after a coordinated
// restart without materially delaying the first collection.
func defaultJitter(interval time.Duration) time.Duration {
	return interval / 10
}

// runTickWithCorrelation injects a fresh tick_id into ctx (re-using the
// request_id key so logging.RequestLogger and HTTPTransport pick it up
// transparently) and recovers from a panic in the tick.
//
// The recover MUST live here, per-tick, not only in safeGo: a panic that
// unwinds past this frame terminates the whole ticker goroutine, silently
// stopping the job (for the shared backfill goroutine, ALL tokens/pairs) until
// process restart. Recovering per tick logs + counts the panic and lets the
// loop keep running; safeGo's recover stays as a last resort.
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
	// Only a tick that reported success advances the gauge. Stamping it after
	// every non-panicking tick made the metric answer "is the goroutine alive",
	// not "is the pipeline working" — a hard-down upstream kept it fresh forever
	// while nothing was ingested, defeating the alert its own doc prescribes.
	//
	// The verdict is logged here rather than left to the tick body: aggregate
	// verdicts (tickOutcome) are built at this level and would otherwise never
	// be surfaced, and the ctx-cancellation exits do not log at all.
	if err := tick(tickCtx); err != nil {
		if logger != nil {
			// A cancelled tick is an ordinary drain, not an incident: every
			// in-flight tick reports one on shutdown, so keep it out of the
			// warning stream while still withholding the success stamp.
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

// tickOutcome tallies per-entity results inside a tick that walks many
// entities, so such a tick can report one honest verdict to runTickerLoop.
//
// Three outcomes, not two. An entity that was SKIPPED — nothing to do, not a
// failure — must not vote, because skips are permanent states here: a backfill
// token that reaches its floor is skipped on every tick forever after. Counting
// that as a success would mean one completed token keeps the job's last-success
// gauge fresh no matter how badly every other token is failing.
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

// verdict reports failure only when NOTHING got through: every entity that
// could act failed, or the tick was cut short (expired budget, shutdown) before
// a single one succeeded. A partial success is real progress and must keep the
// last-success gauge advancing — otherwise one permanently broken entity would
// mask the health of every other one behind the same job. A tick with only
// skips is a healthy idle job.
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
	// Nothing was attempted at all: either there was no work (nil) or the tick
	// was cancelled before it could start.
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
