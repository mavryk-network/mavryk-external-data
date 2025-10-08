package jobs

import (
	"context"
	"log"
	"quotes/internal/config"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/core/infrastructure/storage/repositories"
	"time"

	"gorm.io/gorm"
)

type QuotesCollector struct {
	config     *config.Config
	client     *coingecko.Client
	repository *repositories.QuoteRepository
	ticker     *time.Ticker
	done       chan bool
}

func NewQuotesCollector(cfg *config.Config, db *gorm.DB) *QuotesCollector {
	return &QuotesCollector{
		config:     cfg,
		client:     coingecko.NewClient(cfg.CoinGecko.BaseURL, cfg.CoinGecko.APIKey),
		repository: repositories.NewQuoteRepository(db),
		done:       make(chan bool),
	}
}

func (c *QuotesCollector) Start(ctx context.Context) {
	// Always run backfill if enabled
	if c.config.Backfill.Enabled {
		log.Println("Backfill is enabled - starting catch-up phase before scheduling periodic collection")
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

	log.Printf("Starting quotes collection job with interval: %v", c.config.GetJobInterval())
	c.ticker = time.NewTicker(c.config.GetJobInterval())

	go func() {
		c.collectQuotes(ctx)

		for {
			select {
			case <-c.ticker.C:
				c.collectQuotes(ctx)
			case <-c.done:
				log.Println("Quotes collection job stopped")
				return
			case <-ctx.Done():
				log.Println("Quotes collection job stopped due to context cancellation")
				return
			}
		}
	}()
}

func (c *QuotesCollector) Stop() {
	if c.ticker != nil {
		c.ticker.Stop()
	}
	c.done <- true
}

func (c *QuotesCollector) collectQuotes(ctx context.Context) {
	log.Println("Starting quotes collection...")
	lastTimestamp, err := c.repository.GetLastTimestamp(ctx)
	if err != nil {
		log.Printf("Warning: Could not get last timestamp: %v", err)
		lastTimestamp = time.Now().UTC().Add(-1 * time.Hour)
	}

	from := lastTimestamp.Unix()
	to := time.Now().UTC().Unix()

	if to-from < 60 {
		log.Println("Skipping collection: time range too small")
		return
	}

	currencies := quotes.GetSupportedCurrencies()
	currencyStrings := make([]string, len(currencies))
	for i, currency := range currencies {
		currencyStrings[i] = string(currency)
	}

	currencyData, err := c.client.GetMultipleCurrencies(ctx, currencyStrings, from, to)
	if err != nil {
		log.Printf("Error fetching data from CoinGecko: %v", err)
		return
	}

	quotesList, err := coingecko.MapToQuotes(currencyData)
	if err != nil {
		log.Printf("Error mapping data to quotes: %v", err)
		return
	}

	if len(quotesList) == 0 {
		log.Println("No new quotes to save")
		return
	}

	filteredQuotes := c.filterNewQuotes(ctx, quotesList)
	if len(filteredQuotes) == 0 {
		log.Println("All quotes already exist, skipping save")
		return
	}

	if err := c.repository.SaveBatch(ctx, filteredQuotes); err != nil {
		log.Printf("Error saving quotes: %v", err)
		return
	}

	log.Printf("Successfully collected and saved %d new quotes", len(filteredQuotes))
}

func (c *QuotesCollector) filterNewQuotes(ctx context.Context, quotesList []quotes.Quote) []quotes.Quote {
	if len(quotesList) == 0 {
		return quotesList
	}

	from := quotesList[0].Timestamp
	to := quotesList[len(quotesList)-1].Timestamp

	existingQuotes, err := c.repository.GetQuotes(ctx, from, to, 0)
	if err != nil {
		log.Printf("Warning: Could not check existing quotes: %v", err)
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
	if c.config.Backfill.StartFrom == "" {
		log.Println("Backfill enabled but no start date provided - skipping backfill")
		return nil
	}

	var start time.Time
	// Try RFC3339 first, then fallback to date-only
	if t, err := time.Parse(time.RFC3339, c.config.Backfill.StartFrom); err == nil {
		start = t.UTC()
	} else if t, err2 := time.Parse("2006-01-02", c.config.Backfill.StartFrom); err2 == nil {
		start = t.UTC()
	} else {
		log.Printf("Invalid backfill start date format: %s", c.config.Backfill.StartFrom)
		return nil
	}

	// Determine from based on DB state
	var from time.Time
	lastTs, err := c.repository.GetLastTimestamp(ctx)
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
		log.Println("Backfill up-to-date - skipping")
		return nil
	}

	// Process in chunks (configurable minutes, default 5)
	chunkMinutes := c.config.Backfill.ChunkMinutes
	if chunkMinutes <= 0 {
		chunkMinutes = 5
	}
	chunk := time.Duration(chunkMinutes) * time.Minute
	currencies := quotes.GetSupportedCurrencies()
	currencyStrings := make([]string, len(currencies))
	for i, currency := range currencies {
		currencyStrings[i] = string(currency)
	}

	for from.Before(now) {
		to := from.Add(chunk)
		if to.After(now) {
			to = now
		}

		log.Printf("Backfill chunk: %s -> %s (chunk=%v)", from.Format(time.RFC3339), to.Format(time.RFC3339), chunk)
		data, err := c.client.GetMultipleCurrencies(ctx, currencyStrings, from.Unix(), to.Unix())
		if err != nil {
			log.Printf("Backfill API error, will continue with next chunk: %v", err)
			// move window forward slightly to avoid tight loop
			from = from.Add(15 * time.Minute)
			continue
		}

		mapped, err := coingecko.MapToQuotes(data)
		if err != nil {
			log.Printf("Backfill mapping error: %v", err)
			from = from.Add(15 * time.Minute)
			continue
		}

		// Debug: log raw points by currency to understand sparsity
		totalPoints := 0
		for cur, resp := range data {
			if resp != nil {
				totalPoints += len(resp.Prices)
				log.Printf("Backfill raw points: %s=%d", cur, len(resp.Prices))
			}
		}
		log.Printf("Backfill mapped quotes: %d (raw points total=%d)", len(mapped), totalPoints)

		if len(mapped) > 0 {
			// best-effort idempotency via filter + normal insert
			filtered := c.filterNewQuotes(ctx, mapped)
			if len(filtered) > 0 {
				if err := c.repository.SaveBatch(ctx, filtered); err != nil {
					log.Printf("Backfill save error: %v", err)
				} else {
					log.Printf("Backfill saved %d quotes", len(filtered))
				}
			}
		}

		// Advance window; use last point if available
		if len(mapped) > 0 {
			from = mapped[len(mapped)-1].Timestamp.Add(time.Second)
		} else {
			from = to
		}

		// Respect provider limits (configurable)
		sleepMs := c.config.Backfill.SleepMs
		if sleepMs <= 0 {
			sleepMs = 1100
		}
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	}

	return nil
}
