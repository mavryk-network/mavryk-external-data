package jobs

import (
	"context"
	"quotes/internal/config"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/core/infrastructure/responsecache"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type tokenCollector struct {
	token    quotes.Token
	ticker   *time.Ticker
	client   *coingecko.Client
	stopCh   chan struct{}
	stopOnce sync.Once
}

func (tc *tokenCollector) requestStop() {
	tc.stopOnce.Do(func() { close(tc.stopCh) })
}

type QuotesCollector struct {
	config        *config.Config
	repository    *repositories.QuoteRepository
	responseCache *responsecache.Cache
	collectors    map[string]*tokenCollector
	collectorsWg  sync.WaitGroup
	logger        *zerolog.Logger
}

func NewQuotesCollector(cfg *config.Config, db *gorm.DB, log *zerolog.Logger, responseCache *responsecache.Cache) *QuotesCollector {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	lg := logging.WithComponent(log, "quotes_collector")
	return &QuotesCollector{
		config:        cfg,
		repository:    repositories.NewQuoteRepository(db),
		responseCache: responseCache,
		collectors:    make(map[string]*tokenCollector),
		logger:        lg,
	}
}

func (c *QuotesCollector) Start(ctx context.Context) {
	supportedTokens := quotes.GetSupportedTokens()
	hasBackfill := false
	for _, token := range supportedTokens {
		tokenName := string(token)
		if c.config.IsTokenBackfillEnabled(tokenName) {
			hasBackfill = true
			break
		}
	}

	if hasBackfill {
		c.logger.Info().Msg("backfill_enabled_starting_catch_up")
		if err := c.runBackfill(ctx); err != nil {
			c.logger.Error().Err(err).Msg("backfill_finished_with_error")
		} else {
			c.logger.Info().Msg("backfill_completed_successfully")
		}
	}

	if !c.config.Job.Enabled {
		c.logger.Info().Msg("quotes_collection_job_disabled")
		return
	}
	for _, token := range supportedTokens {
		tokenName := string(token)

		if !c.config.IsTokenEnabled(tokenName) {
			c.logger.Info().Str("token", tokenName).Msg("token_disabled_skipping")
			continue
		}

		tokenCfg := c.config.GetTokenConfig(tokenName)
		interval := c.config.GetTokenInterval(tokenName)
		timeout := c.config.GetTokenTimeout(tokenName)

		c.logger.Info().
			Str("token", tokenName).
			Dur("interval", interval).
			Dur("timeout", timeout).
			Msg("starting_token_collector")

		client := coingecko.NewClient(c.config.CoinGecko.BaseURL, c.config.CoinGecko.APIKey, timeout, c.logger, &c.config.API)

		ticker := time.NewTicker(interval)

		collector := &tokenCollector{
			token:  token,
			ticker: ticker,
			client: client,
			stopCh: make(chan struct{}),
		}

		c.collectors[tokenName] = collector

		c.collectorsWg.Add(1)
		go func(col *tokenCollector, tokenCfg config.TokenConfig) {
			defer c.collectorsWg.Done()
			defer col.ticker.Stop()
			c.startTokenCollector(ctx, col, tokenCfg)
		}(collector, tokenCfg)
	}
}

func (c *QuotesCollector) startTokenCollector(ctx context.Context, collector *tokenCollector, tokenCfg config.TokenConfig) {
	tokenName := string(collector.token)

	c.collectQuotesForToken(ctx, collector.token, collector.client, tokenCfg)

	for {
		select {
		case <-collector.ticker.C:
			c.collectQuotesForToken(ctx, collector.token, collector.client, tokenCfg)
		case <-collector.stopCh:
			c.logger.Info().Str("token", tokenName).Msg("token_collector_stopped")
			return
		case <-ctx.Done():
			c.logger.Info().Str("token", tokenName).Msg("token_collector_stopped_context_cancelled")
			return
		}
	}
}

// Stop requests all token collectors to finish and blocks until their goroutines return.
// Call the cancel function for the context passed to Start before Stop(), so in-flight
// HTTP and database operations observe cancellation promptly.
func (c *QuotesCollector) Stop() {
	for tokenName, collector := range c.collectors {
		collector.requestStop()
		c.logger.Info().Str("token", tokenName).Msg("stopped_collector_for_token")
	}
	c.collectorsWg.Wait()
}

func (c *QuotesCollector) collectQuotesForToken(ctx context.Context, token quotes.Token, client *coingecko.Client, tokenCfg config.TokenConfig) {
	tokenName := string(token)
	c.logger.Info().Str("token", tokenName).Msg("starting_quotes_collection")

	if err := ctx.Err(); err != nil {
		return
	}

	lastTimestamp, err := c.repository.GetLastTimestamp(ctx, tokenName)
	if err != nil {
		c.logger.Warn().Err(err).Str("token", tokenName).Msg("could_not_get_last_timestamp")
		lastTimestamp = time.Now().UTC().Add(-1 * time.Hour)
	}

	from := lastTimestamp.Unix()
	to := time.Now().UTC().Unix()

	minTimeRange := tokenCfg.MinTimeRangeSeconds
	if minTimeRange == 0 {
		minTimeRange = 60 // default
	}

	if to-from < int64(minTimeRange) {
		c.logger.Info().
			Str("token", tokenName).
			Int("min_time_range_seconds", minTimeRange).
			Msg("skipping_collection_time_range_too_small")
		return
	}

	currencies := quotes.GetSupportedCurrencies()

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		c.logger.Error().Str("token", tokenName).Msg("no_coingecko_id_for_token")
		return
	}

	currencyData, err := client.GetMultipleCurrencies(ctx, coinID, currencies, from, to)
	if err != nil {
		c.logger.Error().Err(err).Str("token", tokenName).Msg("coingecko_fetch_error")
		return
	}

	quotesList, err := coingecko.MapToQuotes(currencyData)
	if err != nil {
		c.logger.Error().Err(err).Str("token", tokenName).Msg("map_to_quotes_error")
		return
	}

	if len(quotesList) == 0 {
		c.logger.Info().Str("token", tokenName).Msg("no_new_quotes_to_save")
		return
	}

	inserted, err := c.repository.SaveBatch(ctx, quotesList, tokenName)
	if err != nil {
		c.logger.Error().Err(err).Str("token", tokenName).Msg("save_quotes_error")
		return
	}
	if inserted > 0 && c.responseCache != nil {
		c.responseCache.InvalidateToken(tokenName)
	}

	c.logger.Info().
		Str("token", tokenName).
		Int("batch_size", len(quotesList)).
		Int64("inserted_rows", inserted).
		Msg("quotes_collected_and_saved")
}

func (c *QuotesCollector) runBackfill(ctx context.Context) error {
	supportedTokens := quotes.GetSupportedTokens()
	var wg sync.WaitGroup
	for _, token := range supportedTokens {
		tokenName := string(token)

		if !c.config.IsTokenBackfillEnabled(tokenName) {
			c.logger.Info().Str("token", tokenName).Msg("backfill_disabled_skipping")
			continue
		}

		startFrom := c.config.GetTokenBackfillStartFrom(tokenName)
		if startFrom == "" {
			c.logger.Info().Str("token", tokenName).Msg("backfill_enabled_no_start_date_skipping")
			continue
		}

		var start time.Time
		if t, err := time.Parse(time.RFC3339, startFrom); err == nil {
			start = t.UTC()
		} else if t, err2 := time.Parse("2006-01-02", startFrom); err2 == nil {
			start = t.UTC()
		} else {
			c.logger.Warn().
				Str("token", tokenName).
				Str("start_from", startFrom).
				Msg("invalid_backfill_start_date_format")
			continue
		}

		wg.Add(1)
		go func(t quotes.Token, startTime time.Time) {
			defer wg.Done()
			if err := c.runBackfillForToken(ctx, t, startTime); err != nil {
				c.logger.Error().Err(err).Str("token", string(t)).Msg("backfill_token_error")
			}
		}(token, start)
	}
	wg.Wait()

	return nil
}

func (c *QuotesCollector) runBackfillForToken(ctx context.Context, token quotes.Token, start time.Time) error {
	tokenName := string(token)
	c.logger.Info().Str("token", tokenName).Msg("starting_backfill")

	var from time.Time
	lastTs, err := c.repository.GetLastTimestamp(ctx, tokenName)
	if err != nil {
		from = start
	} else {
		if lastTs.After(start) {
			from = lastTs
		} else {
			from = start
		}
	}

	now := time.Now().UTC()
	if !from.Before(now.Add(-60 * time.Second)) {
		c.logger.Info().Str("token", tokenName).Msg("backfill_up_to_date_skipping")
		return nil
	}

	tokenCfg := c.config.GetTokenConfig(tokenName)

	chunkMinutes := tokenCfg.Backfill.ChunkMinutes
	if chunkMinutes <= 0 {
		chunkMinutes = c.config.Backfill.ChunkMinutes
		if chunkMinutes <= 0 {
			chunkMinutes = 5
		}
	}
	chunk := time.Duration(chunkMinutes) * time.Minute
	currencies := quotes.GetSupportedCurrencies()

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		c.logger.Error().Str("token", tokenName).Msg("no_coingecko_id_for_token_backfill")
		return nil
	}

	timeout := c.config.GetTokenTimeout(tokenName)
	client := coingecko.NewClient(c.config.CoinGecko.BaseURL, c.config.CoinGecko.APIKey, timeout, c.logger, &c.config.API)

	for from.Before(now) {
		if err := ctx.Err(); err != nil {
			return err
		}

		to := from.Add(chunk)
		if to.After(now) {
			to = now
		}

		c.logger.Info().
			Str("token", tokenName).
			Str("from", from.Format(time.RFC3339)).
			Str("to", to.Format(time.RFC3339)).
			Dur("chunk", chunk).
			Msg("backfill_chunk")

		data, err := client.GetMultipleCurrencies(ctx, coinID, currencies, from.Unix(), to.Unix())
		if err != nil {
			c.logger.Warn().Err(err).Str("token", tokenName).Msg("backfill_api_error_continuing")
			from = from.Add(15 * time.Minute)
			continue
		}

		mapped, err := coingecko.MapToQuotes(data)
		if err != nil {
			c.logger.Warn().Err(err).Str("token", tokenName).Msg("backfill_mapping_error")
			from = from.Add(15 * time.Minute)
			continue
		}

		totalPoints := 0
		for cur, resp := range data {
			if resp != nil {
				totalPoints += len(resp.Prices)
				c.logger.Debug().
					Str("token", tokenName).
					Str("currency", string(cur)).
					Int("points", len(resp.Prices)).
					Msg("backfill_raw_points")
			}
		}
		c.logger.Info().
			Str("token", tokenName).
			Int("mapped", len(mapped)).
			Int("raw_points_total", totalPoints).
			Msg("backfill_mapped_quotes")

		if len(mapped) > 0 {
			inserted, err := c.repository.SaveBatch(ctx, mapped, tokenName)
			if err != nil {
				c.logger.Error().Err(err).Str("token", tokenName).Msg("backfill_save_error")
			} else {
				if inserted > 0 && c.responseCache != nil {
					c.responseCache.InvalidateToken(tokenName)
				}
				c.logger.Info().
					Str("token", tokenName).
					Int("batch_size", len(mapped)).
					Int64("inserted_rows", inserted).
					Msg("backfill_saved_quotes")
			}
		}

		if len(mapped) > 0 {
			from = mapped[len(mapped)-1].Timestamp.Add(time.Second)
		} else {
			from = to
		}

		sleepMs := tokenCfg.Backfill.SleepMs
		if sleepMs <= 0 {
			sleepMs = c.config.Backfill.SleepMs
			if sleepMs <= 0 {
				sleepMs = 1100
			}
		}
		d := time.Duration(sleepMs) * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}

	return nil
}
