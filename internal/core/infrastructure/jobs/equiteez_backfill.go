package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
	"github.com/sony/gobreaker"
)

// caughtUpRecheckInterval is how long a pair rests after the forward walk
// reaches the newest indexed fill. Short enough that post-downtime gaps and a
// brand-new pair's first trades land promptly; long enough that idle pairs cost
// ~one cheap indexed query per interval instead of one per tick.
const caughtUpRecheckInterval = 5 * time.Minute

// pauseUntilNextFill parks a caught-up pair without disabling it: the walk
// resumes from the persisted (CursorTs, CursorID) after caughtUpRecheckInterval. Error
// bookkeeping is reset because reaching the head of the log is a success, not a
// failure. Pure (no I/O) so the decision is unit-testable.
func pauseUntilNextFill(st *repositories.BackfillState, now time.Time) {
	next := now.Add(caughtUpRecheckInterval)
	st.NextAttemptAt = &next
	st.Disabled = false
	st.DisabledReason = ""
	st.ErrorCount = 0
	st.LastError = ""
}

// EquiteezBackfillJob ingests historical orderbook fills from the Equiteez
// indexer's `orderbook_order` event log and writes one `last`-side
// PricePoint per filled order into rwa_quote_prices.
//
// Forward-walks per pair by FILL time — keyset (ended_at, id) — with the cursor
// persisted in backfill_state under (source=equiteez, entity=pair_id). id alone
// is unsafe: it is assigned at order creation, so a resting limit order filling
// after the walk passed its id would be skipped forever.
// Bid/ask reconstruction is out of scope (see ADR-0014 §"Stage 2 deferred").
//
// Concurrency: one ticker iterates pairs sequentially; per-pair errors are
// logged and don't block siblings. Same shape as CoinGeckoBackfillJob.
type EquiteezBackfillJob struct {
	cfg    *config.Config
	repo   apiprices.Repository
	lookup *repositories.LookupRepository
	state  *repositories.BackfillStateRepository
	client *equiteez.Client
	logger *zerolog.Logger

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewEquiteezBackfillJob wires the job. Pairs are loaded fresh on each tick
// (so a manual rwa_pairs flip takes effect without restart).
func NewEquiteezBackfillJob(
	cfg *config.Config,
	repo apiprices.Repository,
	lookup *repositories.LookupRepository,
	state *repositories.BackfillStateRepository,
	log *zerolog.Logger,
) *EquiteezBackfillJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	timeout := time.Duration(cfg.API.TimeoutSeconds) * time.Second
	return &EquiteezBackfillJob{
		cfg:    cfg,
		repo:   repo,
		lookup: lookup,
		state:  state,
		client: equiteez.NewClient(cfg.Equiteez, &cfg.API, timeout, log),
		logger: logging.WithComponent(log, "equiteez_backfill_job"),
		stopCh: make(chan struct{}),
	}
}

// Start spawns the ticker. No-op when backfill or RWA module is disabled,
// or when the indexer URL is missing.
func (j *EquiteezBackfillJob) Start(ctx context.Context) {
	if !j.cfg.Equiteez.Backfill.Enabled {
		j.logger.Info().Msg("equiteez_backfill_disabled_by_config")
		return
	}
	if !j.cfg.RWA.Enabled {
		// Same kill-switch story as CoinGecko backfill — global module flag
		// dominates the per-job toggle.
		j.logger.Info().Msg("equiteez_backfill_skipped_rwa_disabled")
		return
	}
	if j.cfg.Equiteez.IndexerURL == "" {
		j.logger.Warn().Msg("equiteez_backfill_no_indexer_url_skipping")
		return
	}

	// Self-heal: earlier builds permanently disabled pairs on catch-up
	// (disabled_reason=caught_up) or after repeated errors (auto_disabled), so
	// pairs frozen by that behaviour would stay dead across deploys. Resume
	// them from their persisted cursor. Operator/terminal disables
	// (manual, reached_floor) survive.
	if resumed, err := j.state.ClearCaughtUp(ctx, prices.SourceEquiteez); err != nil {
		j.logger.Warn().Err(err).Msg("equiteez_backfill_clear_caught_up_failed")
	} else if resumed > 0 {
		j.logger.Info().Int64("pairs", resumed).Msg("equiteez_backfill_resumed_caught_up_pairs")
	}
	if resumed, err := j.state.ClearAutoDisabled(ctx, prices.SourceEquiteez); err != nil {
		j.logger.Warn().Err(err).Msg("equiteez_backfill_clear_auto_disabled_failed")
	} else if resumed > 0 {
		j.logger.Info().Int64("pairs", resumed).Msg("equiteez_backfill_resumed_auto_disabled_pairs")
	}

	tick := time.Duration(j.cfg.Equiteez.Backfill.TickSeconds) * time.Second
	if tick <= 0 {
		tick = 30 * time.Second
	}
	jitter := time.Duration(j.cfg.Equiteez.Backfill.JitterMs) * time.Millisecond

	j.logger.Info().
		Dur("tick", tick).
		Dur("jitter", jitter).
		Int("batch_size", j.cfg.Equiteez.Backfill.BatchSize).
		Str("start_from", j.cfg.Equiteez.Backfill.StartFrom).
		Msg("equiteez_backfill_starting")

	safeGo(&j.wg, j.logger, "equiteez_backfill", func() {
		runTickerLoop(ctx, j.stopCh, tick, jitter, j.logger, "equiteez_backfill", func(c context.Context) error {
			return j.tickAllPairs(c)
		})
	})
}

// Stop signals the goroutine and waits for it to exit. Idempotent.
func (j *EquiteezBackfillJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// tickAllPairs loads the current pair list and processes each one. Per-pair
// errors are logged and swallowed so one bad pair can't stall the rest.
func (j *EquiteezBackfillJob) tickAllPairs(ctx context.Context) error {
	pairs, err := j.lookup.RWAPairs(ctx)
	if err != nil {
		j.logger.Error().Err(err).Msg("equiteez_backfill_load_pairs_failed")
		return err
	}
	enabled := filterBackfillablePairs(pairs)
	if len(enabled) == 0 {
		j.logger.Debug().Msg("equiteez_backfill_no_enabled_pairs")
		return nil
	}
	var out tickOutcome
	for _, p := range enabled {
		if ctx.Err() != nil {
			break
		}
		stepErr := j.stepPair(ctx, p)
		out.record(stepErr)
		if stepErr != nil && !errors.Is(stepErr, errBackfillCoolingDown) && !errors.Is(stepErr, errBackfillSkipped) {
			j.logger.Warn().Err(stepErr).Int64("pair_id", p.ID).Msg("equiteez_backfill_step_error")
		}
	}
	return out.verdict(ctx, "equiteez backfill tick")
}

// stepPair performs one batch fetch for a single pair and advances the cursor.
func (j *EquiteezBackfillJob) stepPair(ctx context.Context, pair prices.RWAPair) error {
	entityKey := pair.EntityKey()
	logger := j.logger.With().Int64("pair_id", pair.ID).Str("orderbook_addr", pair.OrderbookAddr).Logger()
	source := prices.SourceEquiteez

	start := time.Now()
	defer func() {
		metrics.JobTickDurationSeconds.WithLabelValues("backfill", string(source), entityKey).
			Observe(time.Since(start).Seconds())
	}()

	if pair.EquiteezOrderbookID == nil {
		// Sync hasn't populated the indexer ID yet. Skip silently — a successful
		// SyncRWAPairs run will fix it on the next tick.
		logger.Debug().Msg("equiteez_backfill_skipping_no_orderbook_id")
		return errBackfillSkipped
	}

	st, err := j.state.Get(ctx, source, entityKey)
	if err != nil {
		return fmt.Errorf("read backfill_state: %w", err)
	}
	if st == nil {
		st = &repositories.BackfillState{Source: source, EntityKey: entityKey}
	}
	if st.Disabled {
		logger.Debug().Str("reason", st.DisabledReason).Msg("equiteez_backfill_skipped_disabled")
		return errBackfillSkipped
	}

	now := time.Now().UTC()
	if st.NextAttemptAt != nil && st.NextAttemptAt.After(now) {
		logger.Debug().Time("retry_at", *st.NextAttemptAt).Msg("equiteez_backfill_skipped_backoff")
		// A caught-up pair is parked exactly the same way (pauseUntilNextFill),
		// and that is the healthy steady state of this job — reporting it as a
		// failed attempt would freeze the last-success gauge on ~9 of every 10
		// ticks and alert on a working backfill.
		if errorDrivenPause(st) {
			return errBackfillCoolingDown
		}
		return errBackfillSkipped
	}

	quoteDecimals, ok := lookupQuoteDecimals(pair.QuoteSymbol)
	if !ok {
		// Without decimals we'd write raw smallest-unit prices into the table.
		// Rest and retry rather than permanently disabling under a misleading
		// 'manual' reason: the registry is loaded at startup, so registering the
		// quote token heals this on the next deploy with no operator SQL (the
		// live collector likewise just skips such pairs).
		next := now.Add(backfillErrorCooldown)
		st.NextAttemptAt = &next
		st.LastError = fmt.Sprintf("unknown quote symbol %q in tokens registry", pair.QuoteSymbol)
		if perr := j.state.Upsert(ctx, st); perr != nil {
			logger.Error().Err(perr).Msg("equiteez_backfill_persist_failed_on_error")
		}
		metrics.JobErrorsTotal.WithLabelValues("equiteez_backfill", string(source), entityKey, "unknown_quote").Inc()
		logger.Warn().Str("quote_symbol", pair.QuoteSymbol).Msg("equiteez_backfill_paused_unknown_quote")
		// Reported as a failure, matching every tick of the cooldown this just
		// created: the same unchanging registry gap must not read as success on
		// the tick that discovers it and failure on the 59 that follow.
		return errBackfillCoolingDown
	}

	startFrom, err := parseBackfillTime(j.cfg.Equiteez.Backfill.StartFrom)
	if err != nil {
		return fmt.Errorf("invalid equiteez.backfill.start_from: %w", err)
	}

	// Keyset cursor over FILL time. A nil CursorTs means either a fresh pair or a
	// row written by the old id-only walk: in both cases we deliberately ignore
	// the legacy CursorID and restart from start_from. That one-off re-walk is
	// what recovers the late-filled orders the id-only cursor skipped, and it is
	// safe because rwa_quote_prices upserts on (pair_id, side, ts).
	var cursor equiteez.OrderCursor
	if st.CursorTs != nil {
		cursor.EndedAt = *st.CursorTs
		if st.CursorID != nil {
			cursor.ID = *st.CursorID
		}
	}

	batch := j.cfg.Equiteez.Backfill.BatchSize
	if batch <= 0 {
		batch = 200
	}

	logger.Info().
		Time("since_ts", cursor.EndedAt).
		Int64("since_id", cursor.ID).
		Int("batch", batch).
		Str("start_from", startFrom.Format(time.RFC3339)).
		Msg("equiteez_backfill_step")

	orderbookID := int64(*pair.EquiteezOrderbookID)
	orders, err := j.client.GetFilledOrderbookOrders(ctx, orderbookID, cursor, startFrom, batch)
	if err != nil {
		return j.recordError(ctx, &logger, st, err)
	}
	if len(orders) == 0 {
		// Caught up with the indexer — but a forward walk has NO terminal state:
		// new fills keep arriving. Pause until the next re-check instead of
		// disabling the pair, otherwise (a) fills that land during any downtime
		// are never ingested (the live collector snapshots bid/ask/last, it does
		// not replay orderbook_order) and (b) a pair that has not traded yet
		// would be killed on its very first tick and never pick up its first
		// trades. The re-check costs one indexed query returning zero rows.
		pauseUntilNextFill(st, time.Now().UTC())
		logger.Debug().Time("retry_at", *st.NextAttemptAt).Msg("equiteez_backfill_caught_up_paused")
		if err := j.state.Upsert(ctx, st); err != nil {
			return fmt.Errorf("persist caught-up pause: %w", err)
		}
		return nil
	}

	points := ordersToLastPoints(pair, orders, quoteDecimals)
	if len(points) > 0 {
		n, sErr := j.repo.Save(ctx, points)
		if sErr != nil {
			return j.recordError(ctx, &logger, st, fmt.Errorf("save: %w", sErr))
		}
		metrics.JobRowsAffectedTotal.WithLabelValues("backfill", string(source), entityKey).
			Add(float64(n))
		logger.Info().
			Int("orders", len(orders)).
			Int("points", len(points)).
			Int64("rows_affected", n).
			Msg("equiteez_backfill_saved")
		// Materialize the batch's time span into the rwa_quote_prices_* continuous
		// aggregates so backfilled RWA history shows on charts / ATH. Bound the
		// refresh by the points' own min/max ts (a batch spans whatever fill
		// window it covers). Best-effort — a refresh failure must not fail or
		// retry the step.
		if lo, hi := pointsTimeSpan(points); !lo.IsZero() {
			if rErr := j.lookup.RefreshRWACandleAggregates(ctx, lo, hi.Add(time.Second)); rErr != nil {
				// Warn, not Debug — see backfill_cagg_refresh_failed.
				logger.Warn().Err(rErr).Msg("equiteez_backfill_cagg_refresh_failed")
			}
		}
	} else {
		// All orders in this batch had non-positive prices (defensive — shouldn't
		// happen with the fulfilled_amount > 0 filter, but the mapping function
		// guards against zero/negative just in case).
		logger.Info().Int("orders", len(orders)).Msg("equiteez_backfill_no_mappable_points")
	}

	next, ok := advanceOrderCursor(orders)
	if !ok {
		// EVERY row in this batch had an unparseable ended_at — a systemic
		// upstream break, not one poisoned row (a single bad row is skipped and
		// the cursor still advances to the newest parseable one). There is no
		// keyset position to move to, and advancing blindly would silently drop
		// real fills, so retry under backoff/cooldown. Not counted as a drop:
		// the rows are retried, not discarded.
		return j.recordError(ctx, &logger, st,
			fmt.Errorf("batch of %d orders has no parseable ended_at; cannot advance cursor", len(orders)))
	}
	endedAt := next.EndedAt
	id := next.ID
	st.CursorTs = &endedAt
	st.CursorID = &id
	st.ErrorCount = 0
	st.LastError = ""
	st.NextAttemptAt = nil
	if err := j.state.Upsert(ctx, st); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	return nil
}

// advanceOrderCursor returns the greatest (ended_at, id) in the batch — the
// keyset position the next fetch resumes strictly after. ok=false when no row
// carried a parseable ended_at.
//
// It takes the max rather than the last element so a mis-ordered upstream
// response cannot rewind the cursor and cause an endless re-fetch loop.
func advanceOrderCursor(orders []equiteez.OrderbookOrder) (equiteez.OrderCursor, bool) {
	var out equiteez.OrderCursor
	found := false
	for _, o := range orders {
		ts, err := time.Parse(time.RFC3339, o.EndedAt)
		if err != nil {
			continue
		}
		ts = ts.UTC()
		if !found || ts.After(out.EndedAt) || (ts.Equal(out.EndedAt) && o.ID > out.ID) {
			out = equiteez.OrderCursor{EndedAt: ts, ID: o.ID}
			found = true
		}
	}
	return out, found
}

// recordError tracks a failed step: breaker-open pauses without counting, and
// crossing the error threshold converts into a long cooldown (never a
// permanent disable). Always returns the original error.
//
// Mirrors CoinGeckoBackfillJob.recordError for behavioural symmetry.
func (j *EquiteezBackfillJob) recordError(
	ctx context.Context,
	logger *zerolog.Logger,
	st *repositories.BackfillState,
	fetchErr error,
) error {
	st.LastError = truncateError(fetchErr)

	if errors.Is(fetchErr, gobreaker.ErrOpenState) || errors.Is(fetchErr, gobreaker.ErrTooManyRequests) {
		next := time.Now().UTC().Add(breakerOpenRetryDelay)
		st.NextAttemptAt = &next
		if perr := j.state.Upsert(ctx, st); perr != nil {
			logger.Error().Err(perr).Msg("equiteez_backfill_persist_failed_on_error")
		}
		metrics.JobErrorsTotal.WithLabelValues("backfill", string(st.Source), st.EntityKey, "breaker_open").Inc()
		logger.Warn().Err(fetchErr).Msg("equiteez_backfill_paused_breaker_open")
		return fetchErr
	}

	st.ErrorCount++
	metrics.JobErrorsTotal.WithLabelValues("backfill", string(st.Source), st.EntityKey, "transient").Inc()

	threshold := j.cfg.Equiteez.Backfill.BackfillMaxErrors
	if threshold > 0 && st.ErrorCount >= threshold {
		next := time.Now().UTC().Add(backfillErrorCooldown)
		st.NextAttemptAt = &next
		st.ErrorCount = 0
		if perr := j.state.Upsert(ctx, st); perr != nil {
			logger.Error().Err(perr).Msg("equiteez_backfill_persist_failed_after_cooldown")
		}
		metrics.BackfillAutoDisabledTotal.WithLabelValues(string(st.Source), st.EntityKey, "errors_threshold").Inc()
		logger.Error().
			Err(fetchErr).
			Int("threshold", threshold).
			Dur("cooldown", backfillErrorCooldown).
			Msg("equiteez_backfill_cooldown_after_repeated_errors")
		return fetchErr
	}

	backoff := computeBackoff(
		st.ErrorCount,
		j.cfg.Equiteez.Backfill.BackoffInitialMs,
		j.cfg.Equiteez.Backfill.BackoffMaxMs,
		j.cfg.Equiteez.Backfill.MaxBackoffMs,
	)
	next := time.Now().UTC().Add(backoff)
	st.NextAttemptAt = &next
	if perr := j.state.Upsert(ctx, st); perr != nil {
		logger.Error().Err(perr).Msg("equiteez_backfill_persist_failed_on_error")
	}
	logger.Warn().
		Err(fetchErr).
		Int("error_count", st.ErrorCount).
		Dur("backoff", backoff).
		Msg("equiteez_backfill_transient_error")
	return fetchErr
}

// filterBackfillablePairs keeps enabled Equiteez pairs sorted by ID for a
// stable per-tick order (helps log diffing during incident triage).
func filterBackfillablePairs(in []prices.RWAPair) []prices.RWAPair {
	out := make([]prices.RWAPair, 0, len(in))
	for _, p := range in {
		if !p.Enabled || p.Source != prices.SourceEquiteez {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID < out[k].ID })
	return out
}

// ordersToLastPoints maps a batch of orderbook_order rows to PricePoints with
// metric=`last`. Each filled order contributes one point at ts=ended_at and
// price = price_per_rwa_token / 10^quoteDecimals (decimal-exact via .Shift).
//
// Orders with non-parseable ended_at, zero/negative prices, or empty payloads
// are skipped silently — they shouldn't occur given the upstream WHERE clause,
// but defensive filtering keeps a single bad row from failing the whole batch.
//
// In-batch dedup by ts: multiple orders can share an `ended_at` (same block,
// same wall-clock), which would produce two PricePoints with the same
// `(pair_id, side, ts)` PK and break Postgres' ON CONFLICT in a single
// INSERT. We keep the most recent by `orderbook_order.id` — orders arrive
// id-ASC, so a later occurrence of the same ts simply overwrites the
// earlier one.
func ordersToLastPoints(pair prices.RWAPair, orders []equiteez.OrderbookOrder, quoteDecimals int) []prices.PricePoint {
	entityKey := strconv.FormatInt(pair.ID, 10)
	out := make([]prices.PricePoint, 0, len(orders))
	indexByTs := make(map[int64]int, len(orders))
	shift := -int32(quoteDecimals) //nolint:gosec // decimals is small (typically 6); int→int32 cannot overflow
	for _, o := range orders {
		price, reason, ok := mappablePrice(o.PricePerRWAToken, shift)
		if !ok {
			if reason != "" {
				metrics.IngestRowsDroppedTotal.
					WithLabelValues(string(pair.Source), entityKey, reason).Inc()
			}
			continue
		}
		ts, err := time.Parse(time.RFC3339, o.EndedAt)
		if err != nil {
			continue
		}
		pt := prices.PricePoint{
			Source:    pair.Source,
			EntityKey: entityKey,
			Timestamp: ts.UTC(),
			Metric:    string(prices.SideLast),
			Price:     price,
		}
		key := pt.Timestamp.UnixNano()
		if idx, ok := indexByTs[key]; ok {
			out[idx] = pt
			continue
		}
		indexByTs[key] = len(out)
		out = append(out, pt)
	}
	return out
}

// pointsTimeSpan returns the min and max Timestamp across points (both zero when
// points is empty). Used to bound the continuous-aggregate refresh window for a
// batch that was walked by order id rather than by time.
func pointsTimeSpan(points []prices.PricePoint) (min, max time.Time) {
	for i, p := range points {
		if i == 0 || p.Timestamp.Before(min) {
			min = p.Timestamp
		}
		if i == 0 || p.Timestamp.After(max) {
			max = p.Timestamp
		}
	}
	return min, max
}
