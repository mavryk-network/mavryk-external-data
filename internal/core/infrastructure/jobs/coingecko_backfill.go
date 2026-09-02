package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
)

// backfillErrorCooldown is how long an entity rests after BackfillMaxErrors
// consecutive failures — a rest, never a permanent disable, so a transient
// provider outage cannot kill backfill for good.
const backfillErrorCooldown = 30 * time.Minute

// breakerOpenRetryDelay paces retries while the shared breaker is open.
const breakerOpenRetryDelay = 30 * time.Second

// CoinGeckoBackfillJob walks oldest_ts backwards from the live cursor towards
// start_from (clamped by min_start_from). One step per token per tick.
//
// Persistent per-token state (backfill_state, keyed by (source=coingecko, token))
// survives restarts; sticky `disabled` flag with reasons {reached_floor, manual}
// prevents re-flooding the API after terminal events.
type CoinGeckoBackfillJob struct {
	cfg     *config.Config
	repo    apiprices.Repository
	tokenRO *repositories.TokenPriceRepository
	state   *repositories.BackfillStateRepository
	logger  *zerolog.Logger

	clients map[string]*coingecko.Client
	tokens  []prices.TokenInfo

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewCoinGeckoBackfillJob constructs the job. Token clients are materialized on
// Start (so reload semantics stay clean if we later add hot-reload).
func NewCoinGeckoBackfillJob(
	cfg *config.Config,
	repo apiprices.Repository,
	tokenRO *repositories.TokenPriceRepository,
	state *repositories.BackfillStateRepository,
	log *zerolog.Logger,
) *CoinGeckoBackfillJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	return &CoinGeckoBackfillJob{
		cfg:     cfg,
		repo:    repo,
		tokenRO: tokenRO,
		state:   state,
		logger:  logging.WithComponent(log, "coingecko_backfill_job"),
		clients: make(map[string]*coingecko.Client),
		stopCh:  make(chan struct{}),
	}
}

// Start spawns the single ticker goroutine. No-op when no token has backfill
// enabled — keeps shutdown semantics symmetric.
func (j *CoinGeckoBackfillJob) Start(ctx context.Context) {
	tokens := j.enabledBackfillTokens()
	if len(tokens) == 0 {
		j.logger.Info().Msg("backfill_no_enabled_tokens")
		return
	}
	j.tokens = tokens

	// Self-heal rows permanently disabled by older builds' error threshold.
	if resumed, err := j.state.ClearAutoDisabled(ctx, prices.SourceCoinGecko); err != nil {
		j.logger.Warn().Err(err).Msg("backfill_clear_auto_disabled_failed")
	} else if resumed > 0 {
		j.logger.Info().Int64("tokens", resumed).Msg("backfill_resumed_auto_disabled_tokens")
	}

	for _, info := range tokens {
		name := string(info.Symbol)
		j.clients[name] = coingecko.NewClient(j.cfg.CoinGecko, &j.cfg.API, j.cfg.GetTokenTimeout(name), j.logger)
	}

	tick := time.Duration(j.cfg.Backfill.TickSeconds) * time.Second
	if tick <= 0 {
		tick = 5 * time.Second
	}
	jitter := time.Duration(j.cfg.Backfill.JitterMs) * time.Millisecond

	j.logger.Info().
		Dur("tick", tick).
		Dur("jitter", jitter).
		Int("chunk_minutes", j.cfg.Backfill.ChunkMinutes).
		Str("min_start_from", j.cfg.Backfill.MinStartFrom).
		Strs("tokens", tokenNames(tokens)).
		Msg("backfill_job_starting")

	safeGo(&j.wg, j.logger, "backfill", func() {
		runTickerLoop(ctx, j.stopCh, tick, jitter, j.logger, "backfill", func(c context.Context) error {
			return j.tickAllTokens(c)
		})
	})
}

// Stop closes the stop channel and waits. Idempotent.
func (j *CoinGeckoBackfillJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// tickAllTokens processes tokens in a deterministic order; per-token errors are
// logged and swallowed so one failing token cannot block its siblings.
func (j *CoinGeckoBackfillJob) tickAllTokens(ctx context.Context) error {
	var out tickOutcome
	for _, info := range j.tokens {
		if ctx.Err() != nil {
			break
		}
		err := j.stepToken(ctx, info)
		out.record(err)
		if err != nil && shouldLogBackfillStepError(err) {
			j.logger.Warn().Err(err).Str("token", string(info.Symbol)).Msg("backfill_step_error")
		}
	}
	return out.verdict(ctx, "coingecko backfill tick")
}

// stepToken performs one reverse-chunk step for a single token.
func (j *CoinGeckoBackfillJob) stepToken(ctx context.Context, info prices.TokenInfo) error {
	tokenName := string(info.Symbol)
	logger := j.logger.With().Str("token", tokenName).Logger()
	source := prices.SourceCoinGecko
	start := time.Now()
	defer func() {
		metrics.JobTickDurationSeconds.WithLabelValues("backfill", string(source), tokenName).
			Observe(time.Since(start).Seconds())
	}()

	st, err := j.state.Get(ctx, source, tokenName)
	if err != nil {
		return fmt.Errorf("read backfill_state: %w", err)
	}
	if st == nil {
		st = &repositories.BackfillState{Source: source, EntityKey: tokenName}
	}
	if st.Disabled {
		logger.Debug().Str("reason", st.DisabledReason).Msg("backfill_skipped_disabled")
		return errBackfillSkipped
	}

	now := time.Now().UTC()
	if st.NextAttemptAt != nil && st.NextAttemptAt.After(now) {
		logger.Debug().Time("retry_at", *st.NextAttemptAt).Msg("backfill_skipped_backoff")
		if errorDrivenPause(st) {
			return errBackfillCoolingDown
		}
		return errBackfillSkipped
	}

	if st.OldestTs == nil {
		s, sErr := j.tokenRO.OldestTimestamp(ctx, source, tokenName)
		if sErr != nil {
			return fmt.Errorf("read oldest ts: %w", sErr)
		}
		if !s.Found {
			// Live job hasn't produced the first anchor yet.
			logger.Debug().Msg("backfill_awaiting_live_anchor")
			return errBackfillSkipped
		}
		ts := s.TS
		st.OldestTs = &ts
	}

	startFrom, err := parseBackfillTime(j.cfg.GetTokenBackfillStartFrom(tokenName))
	if err != nil {
		return fmt.Errorf("invalid start_from: %w", err)
	}
	hardFloor := startFrom
	if min, mErr := parseBackfillTime(j.cfg.Backfill.MinStartFrom); mErr == nil && !min.IsZero() && min.After(hardFloor) {
		hardFloor = min
	}
	if hardFloor.IsZero() {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonManual,
			"no start_from or min_start_from configured")
	}
	if !st.OldestTs.After(hardFloor) {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedFloor,
			fmt.Sprintf("oldest_ts %s reached floor %s",
				st.OldestTs.UTC().Format(time.RFC3339), hardFloor.Format(time.RFC3339)))
	}

	chunkMinutes := j.cfg.Backfill.ChunkMinutes
	tCfg := j.cfg.GetTokenConfig(tokenName)
	if tCfg.Backfill.ChunkMinutes > 0 {
		chunkMinutes = tCfg.Backfill.ChunkMinutes
	}
	if chunkMinutes <= 0 {
		chunkMinutes = 360
	}
	chunk := time.Duration(chunkMinutes) * time.Minute

	from, to, reached := chunkBounds(*st.OldestTs, hardFloor, chunk)
	if reached {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedFloor,
			"oldest_ts at or below hard_floor")
	}
	if !from.Before(to) {
		// Defensive: clamp may have produced an empty window.
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedFloor,
			"empty backfill window after clamp to floor")
	}

	if info.CoinGeckoID == "" {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonManual,
			"no coingecko id for token")
	}
	client := j.clients[tokenName]
	if client == nil {
		return fmt.Errorf("no coingecko client for %s", tokenName)
	}

	logger.Info().
		Str("from", from.Format(time.RFC3339)).
		Str("to", to.Format(time.RFC3339)).
		Dur("chunk", chunk).
		Msg("backfill_step")

	currencies := prices.AllSupportedCurrencies()
	data, err := client.GetMultipleCurrencies(ctx, info.CoinGeckoID, currencies, from.Unix(), to.Unix())
	if err != nil {
		return j.recordError(ctx, &logger, st, err)
	}
	points := coingecko.MapToPricePoints(source, tokenName, data)
	if len(points) > 0 {
		n, sErr := j.repo.Save(ctx, points)
		if sErr != nil {
			return j.recordError(ctx, &logger, st, fmt.Errorf("save: %w", sErr))
		}
		metrics.JobRowsAffectedTotal.WithLabelValues("backfill", string(source), tokenName).
			Add(float64(n))
		logger.Info().
			Int("batch_size", len(points)).
			Int64("rows_affected", n).
			Msg("backfill_saved")
		// Materialize the just-written chunk into the token_prices_* continuous
		// aggregates. Their refresh policies only cover a recent window
		// (start_offset), so without this backfilled history stays invisible to
		// chart/ATH reads (QueryCandles reads only the CAs). Best-effort: a
		// refresh failure must not fail or retry the backfill step.
		if rErr := j.tokenRO.RefreshCandleAggregates(ctx, from, to); rErr != nil {
			// Warn, not Debug: a persistently failing refresh means backfilled
			// history never reaches the charts, invisible at default log levels.
			logger.Warn().Err(rErr).Msg("backfill_cagg_refresh_failed")
		}
	} else {
		logger.Info().Msg("backfill_empty_window")
	}

	// Advance the cursor regardless of mapped/empty: a window with no provider
	// data must not stall the job forever on the same [from, to].
	fromCopy := from
	st.OldestTs = &fromCopy
	metrics.BackfillOldestTsSeconds.WithLabelValues(string(source), tokenName).
		Set(float64(fromCopy.Unix()))
	st.ErrorCount = 0
	st.LastError = ""
	st.NextAttemptAt = nil

	if !fromCopy.After(hardFloor) {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedFloor,
			"cursor reached floor")
	}
	if err := j.state.Upsert(ctx, st); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	return nil
}

// recordError tracks a failed step via applyBackfillError (shared with the
// Equiteez job); this wrapper persists, counts and logs. Always returns the
// original error.
func (j *CoinGeckoBackfillJob) recordError(
	ctx context.Context,
	logger *zerolog.Logger,
	st *repositories.BackfillState,
	fetchErr error,
) error {
	threshold := j.cfg.Backfill.BackfillMaxErrors
	kind, wait := applyBackfillError(st,
		threshold,
		j.cfg.Backfill.BackoffInitialMs,
		j.cfg.Backfill.BackoffMaxMs,
		j.cfg.Backfill.MaxBackoffMs,
		time.Now().UTC(), fetchErr)
	if perr := j.state.Upsert(ctx, st); perr != nil {
		logger.Error().Err(perr).Msg("backfill_persist_failed_on_error")
	}
	switch kind {
	case backfillErrBreakerPaused:
		metrics.JobErrorsTotal.WithLabelValues("backfill", string(st.Source), st.EntityKey, "breaker_open").Inc()
		logger.Warn().Err(fetchErr).Msg("backfill_paused_breaker_open")
	case backfillErrCooldown:
		metrics.JobErrorsTotal.WithLabelValues("backfill", string(st.Source), st.EntityKey, "transient").Inc()
		metrics.BackfillAutoDisabledTotal.WithLabelValues(string(st.Source), st.EntityKey, "errors_threshold").Inc()
		logger.Error().
			Err(fetchErr).
			Int("threshold", threshold).
			Dur("cooldown", wait).
			Msg("backfill_cooldown_after_repeated_errors")
	default:
		metrics.JobErrorsTotal.WithLabelValues("backfill", string(st.Source), st.EntityKey, "transient").Inc()
		logger.Warn().
			Err(fetchErr).
			Int("error_count", st.ErrorCount).
			Dur("backoff", wait).
			Msg("backfill_transient_error")
	}
	return fetchErr
}

func (j *CoinGeckoBackfillJob) markDisabled(
	ctx context.Context,
	logger *zerolog.Logger,
	st *repositories.BackfillState,
	reason, detail string,
) error {
	st.Disabled = true
	st.DisabledReason = reason
	st.NextAttemptAt = nil
	st.LastError = detail
	if err := j.state.Upsert(ctx, st); err != nil {
		return fmt.Errorf("persist disabled state: %w", err)
	}
	logger.Info().Str("reason", reason).Str("detail", detail).Msg("backfill_token_disabled")
	return nil
}

// enabledBackfillTokens returns the tokens with backfill enabled in config.
// Sorted for deterministic per-tick order.
func (j *CoinGeckoBackfillJob) enabledBackfillTokens() []prices.TokenInfo {
	all := prices.EnabledTokens()
	out := make([]prices.TokenInfo, 0, len(all))
	for _, info := range all {
		name := string(info.Symbol)
		if !j.cfg.IsTokenBackfillEnabled(name) {
			continue
		}
		if strings.TrimSpace(j.cfg.GetTokenBackfillStartFrom(name)) == "" {
			j.logger.Warn().Str("token", name).Msg("backfill_skipping_token_no_start_from")
			continue
		}
		if info.CoinGeckoID == "" {
			j.logger.Warn().Str("token", name).Msg("backfill_skipping_token_no_cg_id")
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].Symbol < out[k].Symbol
	})
	return out
}

// chunkBounds computes one backfill step's window: reached=true when oldestTs
// has met the hard floor, otherwise [max(oldestTs-chunk, hardFloor), oldestTs].
func chunkBounds(oldestTs, hardFloor time.Time, chunk time.Duration) (from, to time.Time, reached bool) {
	if !oldestTs.After(hardFloor) {
		return time.Time{}, time.Time{}, true
	}
	to = oldestTs
	from = oldestTs.Add(-chunk)
	if from.Before(hardFloor) {
		from = hardFloor
	}
	return from, to, false
}

// parseBackfillTime accepts the same formats as validate.go.
func parseBackfillTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("must be RFC3339 or YYYY-MM-DD")
}

// errBackfillCoolingDown marks an entity waiting out an ERROR-driven pause
// (shared by both backfill jobs). Not re-logged by callers, but it does count
// as a failed attempt: a tick where every entity is cooling down accomplished
// nothing and must not stamp a last-success.
var errBackfillCoolingDown = errors.New("backfill entity is in its error cooldown")

// errBackfillSkipped marks an entity with nothing to do — disabled, waiting on
// another job, or not yet wired up. Neither success nor failure: these states
// persist forever, so they abstain from the tick verdict (see tickOutcome).
var errBackfillSkipped = errors.New("backfill entity had nothing to do")

// errorDrivenPause reports whether a future NextAttemptAt was set by a failure
// rather than by a healthy pause.
func errorDrivenPause(st *repositories.BackfillState) bool {
	return st.LastError != ""
}

// computeBackoff returns initialMs * 2^(errorCount-1) clamped to maxMs and the
// hard maxBackoffMs cap; minimum 1s so we never advertise "ready now" right
// after a failure.
func computeBackoff(errorCount, initialMs, maxMs, maxBackoffMs int) time.Duration {
	if errorCount <= 0 {
		errorCount = 1
	}
	if initialMs <= 0 {
		initialMs = 2000
	}
	if maxMs <= 0 {
		maxMs = 60_000
	}
	exp := float64(errorCount - 1)
	if exp > 20 {
		exp = 20
	}
	ms := float64(initialMs) * math.Pow(2, exp)
	if ms > float64(maxMs) {
		ms = float64(maxMs)
	}
	if maxBackoffMs > 0 && ms > float64(maxBackoffMs) {
		ms = float64(maxBackoffMs)
	}
	if ms < 1000 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	const maxLen = 512
	if len(msg) > maxLen {
		msg = msg[:maxLen]
	}
	return msg
}

func tokenNames(tokens []prices.TokenInfo) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = string(t.Symbol)
	}
	return out
}
