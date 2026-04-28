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

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// runTickerLoop runs `tick` once immediately, then on every interval, until the
// context is cancelled or the stop channel closes. Centralizes the boilerplate
// each job otherwise duplicates (immediate first call, ticker, ctx-aware exit,
// per-tick correlation id).
//
// jitter (when > 0) randomizes the FIRST sleep up to ±jitter — good practice for
// multi-replica deployments to avoid synchronized hammering of upstream.
//
// Each tick gets a fresh `tick_id` (uuid) propagated via context so all log
// lines emitted while the tick is running can be correlated post-hoc.
func runTickerLoop(
	ctx context.Context,
	stopCh <-chan struct{},
	interval, jitter time.Duration,
	logger *zerolog.Logger,
	tick func(ctx context.Context),
) {
	if jitter > 0 {
		offset := time.Duration(rand.Int63n(int64(jitter)*2)) - jitter //nolint:gosec // not security-sensitive
		select {
		case <-time.After(offset):
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		}
	}

	runTickWithCorrelation(ctx, tick)

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
			runTickWithCorrelation(ctx, tick)
		}
	}
}

// runTickWithCorrelation injects a fresh tick_id into ctx (re-using the
// request_id key so logging.RequestLogger and HTTPTransport pick it up
// transparently). Cheap (~one uuid alloc per tick) and gives free correlation
// across all log lines from a single iteration.
func runTickWithCorrelation(ctx context.Context, tick func(context.Context)) {
	tickID := "tick-" + uuid.NewString()
	tickCtx := logging.WithRequestID(ctx, tickID)
	tick(tickCtx)
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
