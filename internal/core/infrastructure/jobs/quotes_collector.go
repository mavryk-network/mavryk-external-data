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
	if !c.config.Job.Enabled {
		log.Println("Quotes collection job is disabled")
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
		lastTimestamp = time.Now().Add(-1 * time.Hour)
	}

	from := lastTimestamp.Unix()
	to := time.Now().Unix()

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
