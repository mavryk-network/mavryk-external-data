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
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/core/infrastructure/responsecache"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// BackfillJob walks quotes.oldest_ts from now() backwards towards start_from (clamped
// by backfill.min_start_from). One shared ticker drives one step-per-token per tick,
// deterministic order. Persistent per-token state (backfill_state) survives restarts.
//
// Design mirrors reverseBackfill in mavryk-wallet-backend/validator_index_job.go:
// idempotent forward-only persistence (ON CONFLICT DO NOTHING on the quotes hypertable),
// sticky disabled flag for reached_start_from / auto_disabled, exponential backoff on
// transient errors.
type BackfillJob struct {
	config        *config.Config
	quotes        *repositories.QuoteRepository
	state         *repositories.BackfillStateRepository
	responseCache *responsecache.Cache
	clients       map[string]*coingecko.Client
	tokens        []quotes.Token // tokens with backfill enabled, sorted deterministically
	logger        *zerolog.Logger

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewBackfillJob builds the job struct. No HTTP clients and no goroutines are started
// here — those come up in Start so shutdown paths stay clean even if the job was never
// started (e.g. because backfill is disabled for every token).
func NewBackfillJob(cfg *config.Config, db *gorm.DB, log *zerolog.Logger, responseCache *responsecache.Cache) *BackfillJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	lg := logging.WithComponent(log, "backfill_job")
	return &BackfillJob{
		config:        cfg,
		quotes:        repositories.NewQuoteRepository(db),
		state:         repositories.NewBackfillStateRepository(db),
		responseCache: responseCache,
		clients:       make(map[string]*coingecko.Client),
		logger:        lg,
		stopCh:        make(chan struct{}),
	}
}

// Start launches the single ticker goroutine. Returns immediately. No-op when no token
// has backfill enabled — keeps shutdown semantics symmetric with Stop.
func (j *BackfillJob) Start(ctx context.Context) {
	j.tokens = j.enabledTokens()
	if len(j.tokens) == 0 {
		j.logger.Info().Msg("backfill_job_no_enabled_tokens")
		return
	}

	// Per-token client — timeout is per-token, rate-limit bucket ("coingecko") is
	// shared process-wide so the live job and backfill cannot exceed the configured RPS.
	for _, t := range j.tokens {
		name := string(t)
		timeout := j.config.GetTokenTimeout(name)
		j.clients[name] = coingecko.NewClient(j.config.CoinGecko, &j.config.API, timeout, j.logger)
	}

	tickSeconds := j.config.Backfill.TickSeconds
	if tickSeconds <= 0 {
		tickSeconds = 5
	}
	interval := time.Duration(tickSeconds) * time.Second

	j.logger.Info().
		Dur("tick", interval).
		Int("chunk_minutes", j.config.Backfill.ChunkMinutes).
		Str("min_start_from", j.config.Backfill.MinStartFrom).
		Int("max_errors", j.config.Backfill.BackfillMaxErrors).
		Int("backoff_initial_ms", j.config.Backfill.BackoffInitialMs).
		Int("backoff_max_ms", j.config.Backfill.BackoffMaxMs).
		Strs("tokens", tokenNames(j.tokens)).
		Msg("backfill_job_starting")

	j.wg.Add(1)
	go j.run(ctx, interval)
}

// Stop asks the ticker goroutine to exit and waits for it. Idempotent.
func (j *BackfillJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// run drives the tick loop: one immediate pass on start then one per interval until
// stopCh or ctx fires. A single goroutine keeps token order deterministic and avoids
// thundering-herd on CoinGecko.
func (j *BackfillJob) run(ctx context.Context, interval time.Duration) {
	defer j.wg.Done()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	j.tickAllTokens(ctx)

	for {
		select {
		case <-ctx.Done():
			j.logger.Info().Msg("backfill_job_stopped_context_cancelled")
			return
		case <-j.stopCh:
			j.logger.Info().Msg("backfill_job_stopped")
			return
		case <-ticker.C:
			j.tickAllTokens(ctx)
		}
	}
}

// tickAllTokens walks the deterministic token list. Any per-token error is logged and
// swallowed: one failing token must not block progress of its siblings.
func (j *BackfillJob) tickAllTokens(ctx context.Context) {
	for _, t := range j.tokens {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := j.stepToken(ctx, t); err != nil {
			j.logger.Warn().Err(err).Str("token", string(t)).Msg("backfill_step_error")
		}
	}
}

// stepToken performs one reverse-chunk step for a single token.
// Returns an error only on true failures (DB / state). Transient CoinGecko errors are
// persisted via backoff and returned only for logging visibility.
func (j *BackfillJob) stepToken(ctx context.Context, token quotes.Token) error {
	tokenName := string(token)
	logger := j.logger.With().Str("token", tokenName).Logger()

	st, err := j.state.Get(ctx, tokenName)
	if err != nil {
		return fmt.Errorf("read backfill_state: %w", err)
	}
	if st == nil {
		st = &repositories.BackfillState{Token: tokenName}
	}

	if st.Disabled {
		logger.Debug().Str("reason", st.DisabledReason).Msg("backfill_step_skipped_disabled")
		return nil
	}

	now := time.Now().UTC()
	if st.NextAttemptAt != nil && st.NextAttemptAt.After(now) {
		logger.Debug().Time("retry_at", *st.NextAttemptAt).Msg("backfill_step_skipped_backoff")
		return nil
	}

	if st.OldestTs == nil {
		oldest, oerr := j.quotes.GetOldestTimestamp(ctx, tokenName)
		if oerr != nil {
			// Empty table — the live job has not produced the first anchor yet. Do nothing;
			// we'll pick up oldest_ts on a later tick once live writes land.
			logger.Debug().Err(oerr).Msg("backfill_awaiting_live_anchor")
			return nil
		}
		st.OldestTs = &oldest
	}

	startFrom, err := parseBackfillTime(j.config.GetTokenBackfillStartFrom(tokenName))
	if err != nil {
		return fmt.Errorf("invalid start_from for %s: %w", tokenName, err)
	}
	hardFloor := startFrom
	if min, merr := parseBackfillTime(j.config.Backfill.MinStartFrom); merr == nil && !min.IsZero() && min.After(hardFloor) {
		hardFloor = min
	}
	if hardFloor.IsZero() {
		// Neither start_from nor min_start_from is set. Without a floor we would walk
		// history forever; refuse to start the cursor and flag disabled so the operator
		// sees something actionable in logs/metrics.
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonManual,
			"no start_from or min_start_from configured")
	}

	if !st.OldestTs.After(hardFloor) {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedStartFrom,
			fmt.Sprintf("oldest_ts %s reached hard_floor %s", st.OldestTs.UTC().Format(time.RFC3339), hardFloor.Format(time.RFC3339)))
	}

	chunkMinutes := j.config.Backfill.ChunkMinutes
	tokenCfg := j.config.GetTokenConfig(tokenName)
	if tokenCfg.Backfill.ChunkMinutes > 0 {
		chunkMinutes = tokenCfg.Backfill.ChunkMinutes
	}
	if chunkMinutes <= 0 {
		chunkMinutes = 360
	}
	chunk := time.Duration(chunkMinutes) * time.Minute

	to := *st.OldestTs
	from := to.Add(-chunk)
	if from.Before(hardFloor) {
		from = hardFloor
	}

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonManual,
			"no coingecko id configured for token")
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

	currencies := quotes.GetSupportedCurrencies()
	data, err := client.GetMultipleCurrencies(ctx, coinID, currencies, from.Unix(), to.Unix())
	if err != nil {
		return j.recordError(ctx, &logger, st, err)
	}

	mapped, err := coingecko.MapToQuotes(data)
	if err != nil {
		return j.recordError(ctx, &logger, st, fmt.Errorf("map_to_quotes: %w", err))
	}

	if len(mapped) > 0 {
		inserted, serr := j.quotes.SaveBatch(ctx, mapped, tokenName)
		if serr != nil {
			return j.recordError(ctx, &logger, st, fmt.Errorf("save_batch: %w", serr))
		}
		if inserted > 0 && j.responseCache != nil {
			j.responseCache.InvalidateToken(tokenName)
		}
		logger.Info().
			Int("batch_size", len(mapped)).
			Int64("inserted_rows", inserted).
			Msg("backfill_saved_quotes")
	} else {
		logger.Info().Msg("backfill_empty_window")
	}

	// Advance the cursor regardless of mapped/empty: without this, a window with no data
	// from the provider would stall the job forever on the same [from, to].
	fromCopy := from
	st.OldestTs = &fromCopy
	st.ErrorCount = 0
	st.LastError = ""
	st.NextAttemptAt = nil

	if !fromCopy.After(hardFloor) {
		return j.markDisabled(ctx, &logger, st, repositories.BackfillDisabledReasonReachedStartFrom,
			"cursor reached hard_floor")
	}

	if err := j.state.Upsert(ctx, st); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	return nil
}

// recordError bumps the error counter, computes the exponential backoff, persists the
// new state, and — if the threshold is crossed — flips disabled=true with auto_disabled.
// Always persists and returns the original fetch error for the caller's log.
func (j *BackfillJob) recordError(
	ctx context.Context,
	logger *zerolog.Logger,
	st *repositories.BackfillState,
	fetchErr error,
) error {
	st.ErrorCount++
	st.LastError = truncateError(fetchErr)

	threshold := j.config.Backfill.BackfillMaxErrors
	if threshold > 0 && st.ErrorCount >= threshold {
		st.Disabled = true
		st.DisabledReason = repositories.BackfillDisabledReasonAutoDisabled
		st.NextAttemptAt = nil
		if perr := j.state.Upsert(ctx, st); perr != nil {
			logger.Error().Err(perr).Msg("backfill_state_persist_failed_after_auto_disable")
		}
		logger.Error().
			Err(fetchErr).
			Int("error_count", st.ErrorCount).
			Int("threshold", threshold).
			Msg("backfill_auto_disabled")
		return fetchErr
	}

	backoff := computeBackoff(st.ErrorCount, j.config.Backfill.BackoffInitialMs, j.config.Backfill.BackoffMaxMs)
	next := time.Now().UTC().Add(backoff)
	st.NextAttemptAt = &next
	if perr := j.state.Upsert(ctx, st); perr != nil {
		logger.Error().Err(perr).Msg("backfill_state_persist_failed_on_error")
	}
	logger.Warn().
		Err(fetchErr).
		Int("error_count", st.ErrorCount).
		Dur("backoff", backoff).
		Msg("backfill_transient_error")
	return fetchErr
}

// markDisabled flips the sticky flag with a reason, persists the row, and returns nil —
// "disabled" is a terminal state for the token, not an error to propagate.
func (j *BackfillJob) markDisabled(
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

// enabledTokens returns the deterministic (sorted) list of tokens with backfill
// enabled in the resolved config. Sorting ensures reproducible per-tick order.
func (j *BackfillJob) enabledTokens() []quotes.Token {
	all := quotes.GetSupportedTokens()
	enabled := make([]quotes.Token, 0, len(all))
	for _, t := range all {
		name := string(t)
		if !j.config.IsTokenBackfillEnabled(name) {
			continue
		}
		if strings.TrimSpace(j.config.GetTokenBackfillStartFrom(name)) == "" {
			// validate.go already enforces this invariant at startup, but double-check so a
			// hand-edited config doesn't turn into a spinning request loop.
			j.logger.Warn().Str("token", name).Msg("backfill_skipping_token_no_start_from")
			continue
		}
		enabled = append(enabled, t)
	}
	sort.Slice(enabled, func(i, k int) bool {
		return string(enabled[i]) < string(enabled[k])
	})
	return enabled
}

// parseBackfillTime accepts the same formats as validate.go. Empty input → zero time,
// not an error (callers treat zero as "no floor").
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

// computeBackoff returns initialMs * 2^(errorCount-1) clamped to maxMs, with a 1s
// minimum so we never pretend we're "ready now" right after a failure.
func computeBackoff(errorCount, initialMs, maxMs int) time.Duration {
	if errorCount <= 0 {
		errorCount = 1
	}
	if initialMs <= 0 {
		initialMs = 2000
	}
	if maxMs <= 0 {
		maxMs = 60_000
	}
	// Cap exponent so math.Pow doesn't overflow into Inf for degenerate states.
	exp := float64(errorCount - 1)
	if exp > 20 {
		exp = 20
	}
	ms := float64(initialMs) * math.Pow(2, exp)
	if ms > float64(maxMs) {
		ms = float64(maxMs)
	}
	if ms < 1000 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

// truncateError avoids writing megabyte payloads into last_error by keeping only the
// first line of the message with a hard byte cap.
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

func tokenNames(ts []quotes.Token) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
