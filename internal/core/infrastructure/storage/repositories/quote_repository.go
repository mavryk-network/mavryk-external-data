package repositories

import (
	"context"
	"fmt"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/storage/entities"
	"time"

	"gorm.io/gorm"
)

type QuoteRepository struct {
	db *gorm.DB
}

func NewQuoteRepository(db *gorm.DB) *QuoteRepository {
	return &QuoteRepository{db: db}
}

func (r *QuoteRepository) Save(ctx context.Context, quote quotes.Quote) error {
	entity := &entities.QuoteEntity{
		Timestamp: quote.Timestamp,
		BTC:       quote.BTC,
		USD:       quote.USD,
		EUR:       quote.EUR,
		CNY:       quote.CNY,
		JPY:       quote.JPY,
		KRW:       quote.KRW,
		ETH:       quote.ETH,
		GBP:       quote.GBP,
	}

	result := r.db.WithContext(ctx).Create(entity)
	if result.Error != nil {
		return fmt.Errorf("failed to save quote: %w", result.Error)
	}

	return nil
}

func (r *QuoteRepository) SaveBatch(ctx context.Context, quotesList []quotes.Quote) error {
	if len(quotesList) == 0 {
		return nil
	}

	quoteEntities := make([]entities.QuoteEntity, len(quotesList))
	for i, quote := range quotesList {
		quoteEntities[i] = entities.QuoteEntity{
			Timestamp: quote.Timestamp,
			BTC:       quote.BTC,
			USD:       quote.USD,
			EUR:       quote.EUR,
			CNY:       quote.CNY,
			JPY:       quote.JPY,
			KRW:       quote.KRW,
			ETH:       quote.ETH,
			GBP:       quote.GBP,
		}
	}

	result := r.db.WithContext(ctx).CreateInBatches(quoteEntities, 100)
	if result.Error != nil {
		return fmt.Errorf("failed to save quotes batch: %w", result.Error)
	}

	return nil
}

func (r *QuoteRepository) GetLastQuote(ctx context.Context) (quotes.Quote, error) {
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).Order("timestamp DESC").First(&entity)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return quotes.Quote{}, fmt.Errorf("no quotes found")
		}
		return quotes.Quote{}, fmt.Errorf("failed to get last quote: %w", result.Error)
	}

	return r.entityToDomain(entity), nil
}

func (r *QuoteRepository) GetQuotes(ctx context.Context, from, to time.Time, limit int) ([]quotes.Quote, error) {
	var entities []entities.QuoteEntity
	query := r.db.WithContext(ctx).Where("timestamp >= ? AND timestamp <= ?", from, to).Order("timestamp ASC")
	
	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&entities)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get quotes: %w", result.Error)
	}

	quotesList := make([]quotes.Quote, len(entities))
	for i, entity := range entities {
		quotesList[i] = r.entityToDomain(entity)
	}

	return quotesList, nil
}

func (r *QuoteRepository) GetCount(ctx context.Context) (int64, error) {
	var count int64
	result := r.db.WithContext(ctx).Model(&entities.QuoteEntity{}).Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get quotes count: %w", result.Error)
	}

	return count, nil
}

func (r *QuoteRepository) GetLastTimestamp(ctx context.Context) (time.Time, error) {
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).Select("timestamp").Order("timestamp DESC").First(&entity)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return time.Time{}, fmt.Errorf("no quotes found")
		}
		return time.Time{}, fmt.Errorf("failed to get last timestamp: %w", result.Error)
	}

	return entity.Timestamp, nil
}

func (r *QuoteRepository) entityToDomain(entity entities.QuoteEntity) quotes.Quote {
	return quotes.Quote{
		Timestamp: entity.Timestamp,
		BTC:       entity.BTC,
		USD:       entity.USD,
		EUR:       entity.EUR,
		CNY:       entity.CNY,
		JPY:       entity.JPY,
		KRW:       entity.KRW,
		ETH:       entity.ETH,
		GBP:       entity.GBP,
	}
}
