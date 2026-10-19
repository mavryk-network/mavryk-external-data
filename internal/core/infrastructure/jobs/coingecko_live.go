package jobs

import (
	"context"
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

// maxLiveCatchup caps how far back a live tick may reach to cover an outage
// gap — a catch-up costs one request per vs_currency, and anything older is the
// backfill job's territory. Also the horizon for LatestCommonTimestamp.
const maxLiveCatchup = 24 * time.Hour

// CoinGeckoLiveJob polls CoinGecko on a per-token ticker and writes the freshest
// window into token_prices via the application Repository. It never does
// historical backfill — that is the BackfillJob's concern.
//
// Concurrency: per-token tickers fire independently, but each tick acquires a
// slot from a bounded semaphore (cfg.Job.Concurrency). When all slots are taken,
// later ticks queue up rather than fanning out N parallel CoinGecko calls. 0
// disables the cap (legacy "one goroutine per token, all concurrent").
type CoinGeckoLiveJob struct {
	cfg     *config.Config
	repo    apiprices.Repository
	tokenRO *repositories.TokenPriceRepository
	logger  *zerolog.Logger

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
// tokenRO (optional) lets each tick anchor its window on the last stored point
// of the LAGGIEST collected currency, so outages longer than the lookback —
// including one that hit a single vs_currency — don't leave permanent holes.
func NewCoinGeckoLiveJob(cfg *config.Config, repo apiprices.Repository, tokenRO *repositories.TokenPriceRepository, log *zerolog.Logger) *CoinGeckoLiveJob {
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
		tokenRO:    tokenRO,
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
			// Per-token loop name: a shared "live" label would let one healthy
			// token keep job_last_success fresh while the rest hard-fail.
			runTickerLoop(ctx, j.stopCh, interval, defaultJitter(interval), j.logger, "live:"+name, func(c context.Context) error {
				return j.collectOnce(c, col)
			})
		})
	}
}

// Stop signals every collector and waits for them to exit. Idempotent.
func (j *CoinGeckoLiveJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// collectOnce fetches [now-lookback .. now] and persists new points, extending
// back to the laggiest currency's last stored point (capped at maxLiveCatchup)
// when an outage outlasted the lookback. Returns an error when the tick could
// not do its work; a partial fetch still counts as success.
func (j *CoinGeckoLiveJob) collectOnce(ctx context.Context, col *tokenCollector) error {
	tokenName := string(col.info.Symbol)
	logger := j.logger.With().Str("token", tokenName).Logger()
	if err := ctx.Err(); err != nil {
		return err
	}

	if j.sem != nil {
		select {
		case j.sem <- struct{}{}:
			defer func() { <-j.sem }()
		case <-ctx.Done():
			return ctx.Err()
		case <-j.stopCh:
			// Collected nothing — must not stamp a last-success on the way out.
			return context.Canceled
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
	from := now.Add(-lookback)
	to := now

	currencies := prices.AllSupportedCurrencies()
	if j.tokenRO != nil {
		// Anchor on the LAGGIEST currency: the fetch saves partial results, so
		// the freshest would hold the anchor at ~now and leave one erroring
		// currency's hole unfilled forever.
		s, sErr := j.tokenRO.LatestCommonTimestamp(
			ctx, prices.SourceCoinGecko, tokenName, currencyNames(currencies), now.Add(-maxLiveCatchup))
		switch {
		case sErr != nil:
			// Must be audible: the tick still succeeds on the plain lookback,
			// so a silently failing anchor reopens outage holes with no signal.
			metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName, "anchor").Inc()
			logger.Warn().Err(sErr).Msg("live_anchor_query_failed")
		case s.Found && s.TS.Before(from):
			from = s.TS
			logger.Info().Time("from", from).Msg("live_window_extended_to_laggiest_currency")
		}
	}

	minRange := time.Duration(col.cfg.MinTimeRangeSeconds) * time.Second
	if minRange <= 0 {
		minRange = 60 * time.Second
	}
	if to.Sub(from) < minRange {
		logger.Debug().Msg("live_skip_window_too_small")
		return nil
	}

	data, err := col.client.GetMultipleCurrencies(ctx, col.info.CoinGeckoID, currencies, from.Unix(), to.Unix())
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName, "fetch").Inc()
		if len(data) == 0 {
			logger.Error().Err(err).Msg("live_fetch_failed")
			return err
		}
		// Partial view: save what arrived rather than dropping the whole tick —
		// one dead vs_currency must not stop FT prices (and FX) for the rest.
		logger.Warn().Err(err).Int("currencies_ok", len(data)).Msg("live_fetch_partial")
	}
	points := coingecko.MapToPricePoints(prices.SourceCoinGecko, tokenName, data)
	if len(points) == 0 {
		logger.Debug().Msg("live_no_points")
		return nil
	}
	n, err := j.repo.Save(ctx, points)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName, "save").Inc()
		logger.Error().Err(err).Msg("live_save_failed")
		return err
	}
	metrics.JobRowsAffectedTotal.WithLabelValues("live", string(prices.SourceCoinGecko), tokenName).
		Add(float64(n))
	// Successful tick at Info level — operators need the visible heartbeat per
	// token (the alternative is debugging "why does my graph for MVRK show
	// nothing" through Prometheus). `live_no_points` and `live_skip_*` stay at
	// Debug since they're normal during a quiet upstream window.
	logger.Info().
		Int("batch_size", len(points)).
		Int64("rows_affected", n).
		Msg("live_collected")
	return nil
}

// currencyNames renders the currency set as token_prices.quote_currency keys.
func currencyNames(currencies []prices.Currency) []string {
	out := make([]string, 0, len(currencies))
	for _, c := range currencies {
		out = append(out, string(c))
	}
	return out
}
