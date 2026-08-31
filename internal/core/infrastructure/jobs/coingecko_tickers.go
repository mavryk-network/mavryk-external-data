package jobs

import (
	"context"
	"sync"
	"time"

	"quotes/internal/config"
	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
)

// CoinGeckoTickersJob polls /coins/{token}/tickers on a fixed cadence (default
// 5min) and writes the snapshot into token_tickers via the application
// Repository. Single-token in v1 (MVRK); follow-up TODOS-2 brings DEX phase.
//
// The job is a thin orchestrator: HTTP fetch → mapper → repository.SaveSnapshot.
// All resilience (rate-limit, retry, CB, max-bytes) lives in the CG client's
// transport stack — same one the live FT job uses.
type CoinGeckoTickersJob struct {
	cfg    *config.Config
	repo   apitickers.Repository
	client *coingecko.Client
	logger *zerolog.Logger

	tokenSymbol string
	coinID      string
	source      prices.Source

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewCoinGeckoTickersJob constructs the job. The CG client gets a separate
// timeout because /tickers payloads can be larger than /market_chart/range —
// surfaced via cfg.Tickers.HTTPTimeout.
func NewCoinGeckoTickersJob(cfg *config.Config, repo apitickers.Repository, log *zerolog.Logger) *CoinGeckoTickersJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	logger := logging.WithComponent(log, "coingecko_tickers_job")
	timeout := cfg.Tickers.HTTPTimeout.D()
	client := coingecko.NewClient(cfg.CoinGecko, &cfg.API, timeout, logger)
	return &CoinGeckoTickersJob{
		cfg:         cfg,
		repo:        repo,
		client:      client,
		logger:      logger,
		tokenSymbol: cfg.Tickers.TokenSymbol,
		source:      prices.SourceCoinGecko,
		stopCh:      make(chan struct{}),
	}
}

// Start runs the job in its own goroutine. Returns immediately. Resolves the
// CoinGecko coin id from the runtime token registry — if the token isn't
// registered, the job logs once and exits cleanly (no panics in main).
func (j *CoinGeckoTickersJob) Start(ctx context.Context) {
	if !j.cfg.Tickers.Enabled {
		j.logger.Info().Msg("tickers_job_disabled_by_config")
		return
	}
	if j.tokenSymbol == "" {
		j.logger.Warn().Msg("tickers_job_no_token_configured")
		return
	}
	tok, err := prices.NewToken(j.tokenSymbol)
	if err != nil {
		j.logger.Error().Err(err).Str("token", j.tokenSymbol).Msg("tickers_job_token_not_in_registry")
		return
	}
	info, ok := prices.LookupToken(tok)
	if !ok || info.CoinGeckoID == "" {
		j.logger.Error().Str("token", string(tok)).Msg("tickers_job_no_cg_id_for_token")
		return
	}
	j.coinID = info.CoinGeckoID

	interval := time.Duration(j.cfg.Tickers.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	safeGo(&j.wg, j.logger, "tickers:"+j.tokenSymbol, func() {
		runTickerLoop(ctx, j.stopCh, interval, defaultJitter(interval), j.logger, "tickers", func(c context.Context) error {
			return j.collectOnce(c, tok)
		})
	})
}

// Stop signals the goroutine and waits. Idempotent.
func (j *CoinGeckoTickersJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// collectOnce fetches the live tickers payload and writes the result.
func (j *CoinGeckoTickersJob) collectOnce(ctx context.Context, token prices.Token) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	logger := j.logger.With().Str("token", string(token)).Logger()

	start := time.Now()
	defer func() {
		metrics.JobTickDurationSeconds.
			WithLabelValues("tickers", string(j.source), string(token)).
			Observe(time.Since(start).Seconds())
	}()

	resp, err := j.client.GetTickers(ctx, j.coinID, j.cfg.Tickers.IncludeExchangeLogo)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("tickers", string(j.source), string(token), "fetch").Inc()
		logger.Error().Err(err).Msg("tickers_fetch_failed")
		return err
	}

	now := time.Now().UTC()
	exchanges, rows := coingecko.MapToTickers(resp, j.source, token, now)
	if len(rows) == 0 {
		logger.Debug().Msg("tickers_no_rows_after_mapping")
		return nil
	}

	n, err := j.repo.SaveSnapshot(ctx, exchanges, rows)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("tickers", string(j.source), string(token), "save").Inc()
		logger.Error().Err(err).Msg("tickers_save_failed")
		return err
	}
	metrics.JobRowsAffectedTotal.
		WithLabelValues("tickers", string(j.source), string(token)).
		Add(float64(n))
	// Snapshot gauges — distinct (exchange, target) and distinct exchanges
	// observed in THIS tick. Set unconditionally on a successful save so the
	// gauge reflects the freshest reality, even when n==0 (mapper found rows
	// but the DB had them all already → ON CONFLICT DO NOTHING).
	metrics.TickersActiveCount.
		WithLabelValues(string(j.source), string(token)).
		Set(float64(len(rows)))
	metrics.TickersExchangesCount.
		WithLabelValues(string(j.source), string(token)).
		Set(float64(len(exchanges)))
	logger.Info().
		Int("exchanges", len(exchanges)).
		Int("rows", len(rows)).
		Int64("rows_affected", n).
		Msg("tickers_collected")
	return nil
}
