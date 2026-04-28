package jobs

import (
	"context"
	"sync"
	"time"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
)

// CoinGeckoLiveJob polls CoinGecko on a per-token ticker and writes the freshest
// window into token_prices via the application Repository. It never does
// historical backfill — that is the BackfillJob's concern.
//
// Concurrency: per-token tickers fire independently, but each tick acquires a
// slot from a bounded semaphore (cfg.Job.Concurrency). When all slots are taken,
// later ticks queue up rather than fanning out N parallel CoinGecko calls. 0
// disables the cap (legacy "one goroutine per token, all concurrent").
type CoinGeckoLiveJob struct {
	cfg    *config.Config
	repo   apiprices.Repository
	logger *zerolog.Logger

	tokens     []prices.TokenInfo
	collectors map[string]*tokenCollector
	sem        chan struct{} // nil = unbounded
	stopCh     chan struct{}
	stopOnce   sync.Once
	wg         sync.WaitGroup
}

type tokenCollector struct {
	info   prices.TokenInfo
	client *coingecko.Client
	cfg    config.TokenConfig
}

// NewCoinGeckoLiveJob wires the job. Tokens come from the in-process registry
// (loaded from `tokens` table at startup) — config only filters & sets cadence.
func NewCoinGeckoLiveJob(cfg *config.Config, repo apiprices.Repository, log *zerolog.Logger) *CoinGeckoLiveJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	var sem chan struct{}
	if c := cfg.Job.Concurrency; c > 0 {
		sem = make(chan struct{}, c)
	}
	return &CoinGeckoLiveJob{
		cfg:        cfg,
		repo:       repo,
		logger:     logging.WithComponent(log, "coingecko_live_job"),
		collectors: make(map[string]*tokenCollector),
		sem:        sem,
		stopCh:     make(chan struct{}),
	}
}

// Start spawns one goroutine per enabled token. Each goroutine has its own ticker.
// Returns immediately.
func (j *CoinGeckoLiveJob) Start(ctx context.Context) {
	if !j.cfg.Job.Enabled {
		j.logger.Info().Msg("live_job_disabled_by_config")
		return
	}
	registered := prices.EnabledTokens()
	if len(registered) == 0 {
		j.logger.Warn().Msg("live_job_no_tokens_registered")
		return
	}
	j.tokens = registered

	for _, info := range registered {
		name := string(info.Symbol)
		if !j.cfg.IsTokenEnabled(name) {
			j.logger.Info().Str("token", name).Msg("token_disabled_by_config")
			continue
		}
		if info.CoinGeckoID == "" {
			j.logger.Warn().Str("token", name).Msg("token_missing_cg_id_skipping")
			continue
		}

		tCfg := j.cfg.GetTokenConfig(name)
		client := coingecko.NewClient(j.cfg.CoinGecko, &j.cfg.API, j.cfg.GetTokenTimeout(name), j.logger)
		col := &tokenCollector{info: info, client: client, cfg: tCfg}
		j.collectors[name] = col

		interval := j.cfg.GetTokenInterval(name)
		safeGo(&j.wg, j.logger, "live:"+name, func() {
			runTickerLoop(ctx, j.stopCh, interval, 0, j.logger, func(c context.Context) {
				j.collectOnce(c, col)
			})
		})
	}
}

// Stop signals every collector and waits for them to exit. Idempotent.
func (j *CoinGeckoLiveJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// collectOnce fetches the live window [max(last_ts, now-lookback) .. now] and
// persists any new points. Acquires the global concurrency slot before doing
// any work; releases it on return.
func (j *CoinGeckoLiveJob) collectOnce(ctx context.Context, col *tokenCollector) {
	tokenName := string(col.info.Symbol)
	logger := j.logger.With().Str("token", tokenName).Logger()
	if err := ctx.Err(); err != nil {
		return
	}

	if j.sem != nil {
		select {
		case j.sem <- struct{}{}:
			defer func() { <-j.sem }()
		case <-ctx.Done():
			return
		case <-j.stopCh:
			return
		}
	}

	start := time.Now()
	defer func() {
		metrics.JobTickDurationSeconds.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName).
			Observe(time.Since(start).Seconds())
	}()

	lookback := time.Duration(col.cfg.LiveLookbackSeconds) * time.Second
	if lookback <= 0 {
		lookback = 2 * time.Duration(col.cfg.IntervalSeconds) * time.Second
	}

	now := time.Now().UTC()
	minWindowStart := now.Add(-lookback)

	from := minWindowStart
	to := now

	minRange := time.Duration(col.cfg.MinTimeRangeSeconds) * time.Second
	if minRange <= 0 {
		minRange = 60 * time.Second
	}
	if to.Sub(from) < minRange {
		logger.Debug().Msg("live_skip_window_too_small")
		return
	}

	currencies := prices.AllSupportedCurrencies()
	data, err := col.client.GetMultipleCurrencies(ctx, col.info.CoinGeckoID, currencies, from.Unix(), to.Unix())
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName, "fetch").Inc()
		logger.Error().Err(err).Msg("live_fetch_failed")
		return
	}
	points := coingecko.MapToPricePoints(prices.SourceCoinGecko, tokenName, data)
	if len(points) == 0 {
		logger.Debug().Msg("live_no_points")
		return
	}
	n, err := j.repo.Save(ctx, points)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName, "save").Inc()
		logger.Error().Err(err).Msg("live_save_failed")
		return
	}
	metrics.JobRowsAffectedTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName).
		Add(float64(n))
	// Refactoring v2 §2.4 — Info on tick success is too noisy for steady-state ops.
	logger.Debug().
		Int("batch_size", len(points)).
		Int64("rows_affected", n).
		Msg("live_collected")
}
