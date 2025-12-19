package jobs

import (
	"context"
	"log"
	"quotes/internal/config"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/core/infrastructure/storage/repositories"
	"sync"
	"time"

	"gorm.io/gorm"
)

type tokenCollector struct {
	token   quotes.Token
	ticker  *time.Ticker
	client  *coingecko.Client
	done    chan bool
}

type QuotesCollector struct {
	config     *config.Config
	repository *repositories.QuoteRepository
	collectors map[string]*tokenCollector
	done       chan bool
}

func NewQuotesCollector(cfg *config.Config, db *gorm.DB) *QuotesCollector {
	return &QuotesCollector{
		config:     cfg,
		repository: repositories.NewQuoteRepository(db),
		collectors: make(map[string]*tokenCollector),
		done:       make(chan bool),
	}
}

func (c *QuotesCollector) Start(ctx context.Context) {
	// Run backfill for tokens that have it enabled (either globally or token-specific)
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
		log.Println("Backfill is enabled for some tokens - starting catch-up phase before scheduling periodic collection")
		if err := c.runBackfill(ctx); err != nil {
			log.Printf("Backfill finished with error: %v", err)
		} else {
			log.Println("Backfill completed successfully")
		}
	}

	if !c.config.Job.Enabled {
		log.Println("Quotes collection job is disabled - skipping periodic collection")
		return
	}
	for _, token := range supportedTokens {
		tokenName := string(token)
		
		if !c.config.IsTokenEnabled(tokenName) {
			log.Printf("Token %s is disabled - skipping", tokenName)
			continue
		}

		tokenCfg := c.config.GetTokenConfig(tokenName)
		interval := c.config.GetTokenInterval(tokenName)
		timeout := c.config.GetTokenTimeout(tokenName)

		log.Printf("Starting collector for token %s with interval: %v, timeout: %v", tokenName, interval, timeout)

		client := coingecko.NewClient(c.config.CoinGecko.BaseURL, c.config.CoinGecko.APIKey, timeout)

		ticker := time.NewTicker(interval)
		done := make(chan bool)

		collector := &tokenCollector{
			token:  token,
			ticker: ticker,
			client: client,
			done:   done,
		}

		c.collectors[tokenName] = collector

		go c.startTokenCollector(ctx, collector, tokenCfg)
	}
}

func (c *QuotesCollector) startTokenCollector(ctx context.Context, collector *tokenCollector, tokenCfg config.TokenConfig) {
	tokenName := string(collector.token)
	
	c.collectQuotesForToken(ctx, collector.token, collector.client, tokenCfg)

	for {
		select {
		case <-collector.ticker.C:
			c.collectQuotesForToken(ctx, collector.token, collector.client, tokenCfg)
		case <-collector.done:
			log.Printf("Token collector for %s stopped", tokenName)
			return
		case <-ctx.Done():
			log.Printf("Token collector for %s stopped due to context cancellation", tokenName)
			return
		}
	}
}

func (c *QuotesCollector) Stop() {
	for tokenName, collector := range c.collectors {
		if collector.ticker != nil {
			collector.ticker.Stop()
		}
		collector.done <- true
		log.Printf("Stopped collector for token: %s", tokenName)
	}
	c.done <- true
}

func (c *QuotesCollector) collectQuotesForToken(ctx context.Context, token quotes.Token, client *coingecko.Client, tokenCfg config.TokenConfig) {
	tokenName := string(token)
	log.Printf("Starting quotes collection for token: %s", tokenName)

	lastTimestamp, err := c.repository.GetLastTimestamp(ctx, tokenName)
	if err != nil {
		log.Printf("Warning: Could not get last timestamp for %s: %v", tokenName, err)
		lastTimestamp = time.Now().UTC().Add(-1 * time.Hour)
	}

	from := lastTimestamp.Unix()
	to := time.Now().UTC().Unix()

	minTimeRange := tokenCfg.MinTimeRangeSeconds
	if minTimeRange == 0 {
		minTimeRange = 60 // default
	}

	if to-from < int64(minTimeRange) {
		log.Printf("Skipping collection for %s: time range too small (need at least %d seconds)", tokenName, minTimeRange)
		return
	}

	currencies := quotes.GetSupportedCurrencies()
	currencyStrings := make([]string, len(currencies))
	for i, currency := range currencies {
		currencyStrings[i] = string(currency)
	}

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		log.Printf("Error: No CoinGecko ID found for token %s", tokenName)
		return
	}

	currencyData, err := client.GetMultipleCurrencies(ctx, coinID, currencyStrings, from, to)
	if err != nil {
		log.Printf("Error fetching data from CoinGecko for %s: %v", tokenName, err)
		return
	}

	quotesList, err := coingecko.MapToQuotes(currencyData)
	if err != nil {
		log.Printf("Error mapping data to quotes for %s: %v", tokenName, err)
		return
	}

	if len(quotesList) == 0 {
		log.Printf("No new quotes to save for %s", tokenName)
		return
	}

	filteredQuotes := c.filterNewQuotes(ctx, quotesList, tokenName)
	if len(filteredQuotes) == 0 {
		log.Printf("All quotes already exist for %s, skipping save", tokenName)
		return
	}

	if err := c.repository.SaveBatch(ctx, filteredQuotes, tokenName); err != nil {
		log.Printf("Error saving quotes for %s: %v", tokenName, err)
		return
	}

	log.Printf("Successfully collected and saved %d new quotes for %s", len(filteredQuotes), tokenName)
}

func (c *QuotesCollector) filterNewQuotes(ctx context.Context, quotesList []quotes.Quote, tokenName string) []quotes.Quote {
	if len(quotesList) == 0 {
		return quotesList
	}

	from := quotesList[0].Timestamp
	to := quotesList[len(quotesList)-1].Timestamp

	existingQuotes, err := c.repository.GetQuotes(ctx, from, to, 0, tokenName)
	if err != nil {
		log.Printf("Warning: Could not check existing quotes for %s: %v", tokenName, err)
		return quotesList
	}

	existingTimestamps := make(map[time.Time]bool)
	for _, quote := range existingQuotes {
		existingTimestamps[quote.Timestamp] = true
	}

	var filteredQuotes []quotes.Quote
	for _, quote := range quotesList {
		if !existingTimestamps[quote.Timestamp] {
			filteredQuotes = append(filteredQuotes, quote)
		}
	}

	return filteredQuotes
}

func (c *QuotesCollector) runBackfill(ctx context.Context) error {
	// Run backfill for tokens that have it enabled
	supportedTokens := quotes.GetSupportedTokens()
	var wg sync.WaitGroup
	for _, token := range supportedTokens {
		tokenName := string(token)
		
		// Check if backfill is enabled for this token
		if !c.config.IsTokenBackfillEnabled(tokenName) {
			log.Printf("Backfill disabled for token %s - skipping", tokenName)
			continue
		}

		// Get token-specific backfill start date
		startFrom := c.config.GetTokenBackfillStartFrom(tokenName)
		if startFrom == "" {
			log.Printf("Backfill enabled for token %s but no start date provided - skipping", tokenName)
			continue
		}

		var start time.Time
		// Try RFC3339 first, then fallback to date-only
		if t, err := time.Parse(time.RFC3339, startFrom); err == nil {
			start = t.UTC()
		} else if t, err2 := time.Parse("2006-01-02", startFrom); err2 == nil {
			start = t.UTC()
		} else {
			log.Printf("Invalid backfill start date format for token %s: %s", tokenName, startFrom)
			continue
		}

		wg.Add(1)
		go func(t quotes.Token, startTime time.Time) {
			defer wg.Done()
			if err := c.runBackfillForToken(ctx, t, startTime); err != nil {
				log.Printf("Backfill error for token %s: %v", string(t), err)
			}
		}(token, start)
	}
	wg.Wait()

	return nil
}

func (c *QuotesCollector) runBackfillForToken(ctx context.Context, token quotes.Token, start time.Time) error {
	tokenName := string(token)
	log.Printf("Starting backfill for token: %s", tokenName)

	// Determine from based on DB state
	var from time.Time
	lastTs, err := c.repository.GetLastTimestamp(ctx, tokenName)
	if err != nil {
		// if no data - start from configured
		from = start
	} else {
		if lastTs.After(start) {
			from = lastTs
		} else {
			from = start
		}
	}

	now := time.Now().UTC()
	if !from.Before(now.Add(-60 * time.Second)) { // leave at least 60s gap for live collector
		log.Printf("Backfill up-to-date for %s - skipping", tokenName)
		return nil
	}

	// Get token-specific backfill config
	tokenCfg := c.config.GetTokenConfig(tokenName)
	
	// Process in chunks (use token-specific or global settings)
	chunkMinutes := tokenCfg.Backfill.ChunkMinutes
	if chunkMinutes <= 0 {
		chunkMinutes = c.config.Backfill.ChunkMinutes
		if chunkMinutes <= 0 {
			chunkMinutes = 5
		}
	}
	chunk := time.Duration(chunkMinutes) * time.Minute
	currencies := quotes.GetSupportedCurrencies()
	currencyStrings := make([]string, len(currencies))
	for i, currency := range currencies {
		currencyStrings[i] = string(currency)
	}

	coinID := quotes.GetCoinGeckoID(token)
	if coinID == "" {
		log.Printf("Error: No CoinGecko ID found for token %s", tokenName)
		return nil
	}

	// Create client with token-specific timeout for backfill
	timeout := c.config.GetTokenTimeout(tokenName)
	client := coingecko.NewClient(c.config.CoinGecko.BaseURL, c.config.CoinGecko.APIKey, timeout)

	for from.Before(now) {
		to := from.Add(chunk)
		if to.After(now) {
			to = now
		}

		log.Printf("Backfill chunk for %s: %s -> %s (chunk=%v)", tokenName, from.Format(time.RFC3339), to.Format(time.RFC3339), chunk)
		data, err := client.GetMultipleCurrencies(ctx, coinID, currencyStrings, from.Unix(), to.Unix())
		if err != nil {
			log.Printf("Backfill API error for %s, will continue with next chunk: %v", tokenName, err)
			// move window forward slightly to avoid tight loop
			from = from.Add(15 * time.Minute)
			continue
		}

		mapped, err := coingecko.MapToQuotes(data)
		if err != nil {
			log.Printf("Backfill mapping error for %s: %v", tokenName, err)
			from = from.Add(15 * time.Minute)
			continue
		}

		// Debug: log raw points by currency to understand sparsity
		totalPoints := 0
		for cur, resp := range data {
			if resp != nil {
				totalPoints += len(resp.Prices)
				log.Printf("Backfill raw points for %s: %s=%d", tokenName, cur, len(resp.Prices))
			}
		}
		log.Printf("Backfill mapped quotes for %s: %d (raw points total=%d)", tokenName, len(mapped), totalPoints)

		if len(mapped) > 0 {
			// best-effort idempotency via filter + normal insert
			filtered := c.filterNewQuotes(ctx, mapped, tokenName)
			if len(filtered) > 0 {
				if err := c.repository.SaveBatch(ctx, filtered, tokenName); err != nil {
					log.Printf("Backfill save error for %s: %v", tokenName, err)
				} else {
					log.Printf("Backfill saved %d quotes for %s", len(filtered), tokenName)
				}
			}
		}

		// Advance window; use last point if available
		if len(mapped) > 0 {
			from = mapped[len(mapped)-1].Timestamp.Add(time.Second)
		} else {
			from = to
		}

		// Respect provider limits (use token-specific or global settings)
		sleepMs := tokenCfg.Backfill.SleepMs
		if sleepMs <= 0 {
			sleepMs = c.config.Backfill.SleepMs
			if sleepMs <= 0 {
				sleepMs = 1100
			}
		}
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	return nil
}
