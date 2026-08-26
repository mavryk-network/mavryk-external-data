// Package jobs hosts the background collectors (CoinGecko live + backfill, Equiteez
// RWA orderbook). Each job exposes Start(ctx)/Stop() and writes through the
// application Repository (so the cache decorator invalidates as a side-effect).
package jobs

import (
	"context"
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
func runTickerLoop(
	ctx context.Context,
	stopCh <-chan struct{},
	interval, jitter time.Duration,
	logger *zerolog.Logger,
	name string,
	tick func(ctx context.Context),
) {
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

// runTickWithCorrelation injects a fresh tick_id into ctx (re-using the
// request_id key so logging.RequestLogger and HTTPTransport pick it up
// transparently) and recovers from a panic in the tick.
//
// The recover MUST live here, per-tick, not only in safeGo: a panic that
// unwinds past this frame terminates the whole ticker goroutine, silently
// stopping the job (for the shared backfill goroutine, ALL tokens/pairs) until
// process restart. Recovering per tick logs + counts the panic and lets the
// loop keep running; safeGo's recover stays as a last resort.
func runTickWithCorrelation(ctx context.Context, timeout time.Duration, logger *zerolog.Logger, name string, tick func(context.Context)) {
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
	tick(tickCtx)
	metrics.JobLastSuccessTimestamp.WithLabelValues(name).SetToCurrentTime()
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
