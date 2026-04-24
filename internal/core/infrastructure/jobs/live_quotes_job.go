package jobs

import (
	"context"
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

// liveTokenCollector is one per-token ticker + its HTTP client.
// stopCh is closed exactly once via stopOnce so idempotent shutdowns are safe.
type liveTokenCollector struct {
	token    quotes.Token
	ticker   *time.Ticker
	client   *coingecko.Client
	stopCh   chan struct{}
	stopOnce sync.Once
}

func (tc *liveTokenCollector) requestStop() {
	tc.stopOnce.Do(func() { close(tc.stopCh) })
}

// LiveQuotesJob polls CoinGecko on a per-token ticker and persists only the freshest
// window. It never does historical backfill — that is the BackfillJob's concern. The
// live window is bounded by live_lookback_seconds (default 2× interval) so even a wiped
// DB or a hours-stale last_ts does not turn one tick into a multi-hour fetch.
type LiveQuotesJob struct {
	config        *config.Config
	repository    *repositories.QuoteRepository
	responseCache *responsecache.Cache
	collectors    map[string]*liveTokenCollector
	collectorsWg  sync.WaitGroup
	logger        *zerolog.Logger
}

// NewLiveQuotesJob constructs the live job. Clients are materialized in Start so they
// pick up the current config at the point the job actually starts ticking.
func NewLiveQuotesJob(cfg *config.Config, db *gorm.DB, log *zerolog.Logger, responseCache *responsecache.Cache) *LiveQuotesJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	lg := logging.WithComponent(log, "live_quotes_job")
	return &LiveQuotesJob{
		config:        cfg,
		repository:    repositories.NewQuoteRepository(db),
		responseCache: responseCache,
		collectors:    make(map[string]*liveTokenCollector),
		logger:        lg,
	}
}

// Start spawns one goroutine per enabled token. It returns as soon as the goroutines
// are wired up — no blocking backfill on startup.
func (j *LiveQuotesJob) Start(ctx context.Context) {
	if !j.config.Job.Enabled {
		j.logger.Info().Msg("quotes_collection_job_disabled")
		return
	}

	for _, token := range quotes.GetSupportedTokens() {
		tokenName := string(token)
		if !j.config.IsTokenEnabled(tokenName) {
			j.logger.Info().Str("token", tokenName).Msg("token_disabled_skipping")
			continue
		}

		tokenCfg := j.config.GetTokenConfig(tokenName)
		interval := j.config.GetTokenInterval(tokenName)
		timeout := j.config.GetTokenTimeout(tokenName)
		lookback := j.config.GetTokenLiveLookback(tokenName)

		j.logger.Info().
			Str("token", tokenName).
			Dur("interval", interval).
			Dur("timeout", timeout).
			Dur("live_lookback", lookback).
			Msg("starting_token_collector")

		client := coingecko.NewClient(j.config.CoinGecko, &j.config.API, timeout, j.logger)
		ticker := time.NewTicker(interval)

		collector := &liveTokenCollector{
			token:  token,
			ticker: ticker,
			client: client,
			stopCh: make(chan struct{}),
		}
		j.collectors[tokenName] = collector

		j.collectorsWg.Add(1)
		go func(col *liveTokenCollector, tokenCfg config.TokenConfig) {
			defer j.collectorsWg.Done()
			defer col.ticker.Stop()
			j.runTokenLoop(ctx, col, tokenCfg)
		}(collector, tokenCfg)
	}
}

// Stop asks every token collector to finish and waits for their goroutines to return.
// Callers should cancel the Start context before calling Stop so in-flight HTTP/DB ops
// observe cancellation promptly.
func (j *LiveQuotesJob) Stop() {
	for tokenName, collector := range j.collectors {
		collector.requestStop()
		j.logger.Info().Str("token", tokenName).Msg("stopped_collector_for_token")
	}
	j.collectorsWg.Wait()
}

// runTokenLoop does one immediate collection pass (so there is a fresh row in the DB
// right after Start) and then one collection per ticker tick until the context or
// stopCh fires.
func (j *LiveQuotesJob) runTokenLoop(ctx context.Context, collector *liveTokenCollector, tokenCfg config.TokenConfig) {
	tokenName := string(collector.token)

	j.collectOnce(ctx, collector.token, collector.client, tokenCfg)

	for {
		select {
		case <-collector.ticker.C:
			j.collectOnce(ctx, collector.token, collector.client, tokenCfg)
		case <-collector.stopCh:
			j.logger.Info().Str("token", tokenName).Msg("token_collector_stopped")
			return
		case <-ctx.Done():
			j.logger.Info().Str("token", tokenName).Msg("token_collector_stopped_context_cancelled")
			return
		}
	}
}

// collectOnce fetches the live window [max(last_ts, now-lookback) .. now] for a token
// and persists any new points. Skipped when the window is smaller than
// MinTimeRangeSeconds (avoids nuisance requests right after a previous tick).
func (j *LiveQuotesJob) collectOnce(ctx context.Context, token quotes.Token, client *coingecko.Client, tokenCfg config.TokenConfig) {
	tokenName := string(token)
	if err := ctx.Err(); err != nil {
		return
	}

	j.logger.Info().Str("token", tokenName).Msg("starting_quotes_collection")

	lookback := time.Duration(tokenCfg.LiveLookbackSeconds) * time.Second
	if lookback <= 0 {
		lookback = 2 * time.Duration(tokenCfg.IntervalSeconds) * time.Second
	}

	now := time.Now().UTC()
	minWindowStart := now.Add(-lookback)

	lastTimestamp, err := j.repository.GetLastTimestamp(ctx, tokenName)
	if err != nil {
		// Empty DB or first run: window is exactly the lookback. Backfill will push the
		// oldest_ts cursor back from here over time.
		j.logger.Debug().Err(err).Str("token", tokenName).Msg("no_last_timestamp_using_lookback")
		lastTimestamp = minWindowStart
	}
	if lastTimestamp.Before(minWindowStart) {
		lastTimestamp = minWindowStart
	}

	from := lastTimestamp.Unix()
	to := now.Unix()

	minTimeRange := tokenCfg.MinTimeRangeSeconds
	if minTimeRange == 0 {
		minTimeRange = 60
	}
	if to-from < int64(minTimeRange) {
		j.logger.Info().
			Str("token", tokenName).
			Int("min_time_range_seconds", minTimeRange).
			Msg("skipping_collection_time_range_too_small")
		return
	}

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		j.logger.Error().Str("token", tokenName).Msg("no_coingecko_id_for_token")
		return
	}

	currencyData, err := client.GetMultipleCurrencies(ctx, coinID, quotes.GetSupportedCurrencies(), from, to)
	if err != nil {
		j.logger.Error().Err(err).Str("token", tokenName).Msg("coingecko_fetch_error")
		return
	}

	quotesList, err := coingecko.MapToQuotes(currencyData)
	if err != nil {
		j.logger.Error().Err(err).Str("token", tokenName).Msg("map_to_quotes_error")
		return
	}

	if len(quotesList) == 0 {
		j.logger.Info().Str("token", tokenName).Msg("no_new_quotes_to_save")
		return
	}

	inserted, err := j.repository.SaveBatch(ctx, quotesList, tokenName)
	if err != nil {
		j.logger.Error().Err(err).Str("token", tokenName).Msg("save_quotes_error")
		return
	}
	if inserted > 0 && j.responseCache != nil {
		j.responseCache.InvalidateToken(tokenName)
	}

	j.logger.Info().
		Str("token", tokenName).
		Int("batch_size", len(quotesList)).
		Int64("inserted_rows", inserted).
		Msg("quotes_collected_and_saved")
}
